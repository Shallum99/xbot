package bot

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xdevplatform/xurl/api"
)

// maxMediaFileSize is the maximum size of a single media download (50MB).
const maxMediaFileSize = 50 * 1024 * 1024

// maxMediaURLs is the maximum number of media URLs to process per tweet (M4).
const maxMediaURLs = 10

// defaultMaxRunsPerHour is the default rate limit for agent executions (H4/L3).
const defaultMaxRunsPerHour = 10

// allowedMediaHosts is the set of hosts we allow media downloads from.
// C2: Prevents SSRF by restricting downloads to known X/Twitter media domains.
var allowedMediaHosts = map[string]bool{
	"pbs.twimg.com":   true,
	"video.twimg.com": true,
	"abs.twimg.com":   true,
	"ton.twimg.com":   true,
}

// allowedMediaExtensions is the allowlist of file extensions for downloaded media (M5).
var allowedMediaExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".mp4":  true,
	".webm": true,
	".webp": true,
}

// Handler processes individual tweets through the bot pipeline.
type Handler struct {
	Config *BotConfig
	State  *BotState
	Client api.Client
	Opts   api.RequestOptions
	Agent  Agent
	Logger *log.Logger

	// H4/L3: Rate limiting for agent executions
	rateMu        sync.Mutex
	runTimestamps []time.Time
}

// checkRateLimit checks if agent runs are within the hourly limit (H4/L3).
// Finding #3: Does NOT consume a slot — call recordAgentRun() when the agent actually starts.
func (h *Handler) checkRateLimit() error {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)

	// Prune old timestamps
	valid := h.runTimestamps[:0]
	for _, t := range h.runTimestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	h.runTimestamps = valid

	if len(h.runTimestamps) >= defaultMaxRunsPerHour {
		return fmt.Errorf("rate limit exceeded: %d agent runs in the last hour (max %d)", len(h.runTimestamps), defaultMaxRunsPerHour)
	}

	return nil
}

// recordAgentRun records that an agent execution is starting.
func (h *Handler) recordAgentRun() {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	h.runTimestamps = append(h.runTimestamps, time.Now())
}

// Process handles a single trigger tweet: fetches the parent bug report,
// downloads media, runs the coding agent, and replies with the PR link.
func (h *Handler) Process(ctx context.Context, trigger ParsedTweet) error {
	h.Logger.Printf("[PROCESSING] Tweet %s from @%s: %s", trigger.ID, trigger.AuthorUsername, truncate(trigger.Text, 100))

	// M6: Validate tweet ID is numeric before using as branch name
	if !numericID.MatchString(trigger.ID) {
		return fmt.Errorf("invalid tweet ID %q: must be numeric", trigger.ID)
	}

	// H4/L3: Check rate limit before proceeding
	if err := h.checkRateLimit(); err != nil {
		h.Logger.Printf("[RATE-LIMIT] %v", err)
		return err
	}

	// 1. Fetch parent tweet (the actual bug report) if this is a reply
	var bugText string
	var mediaURLs []string
	var bugAuthor string

	if trigger.InReplyToID != "" {
		parent, err := FetchParentTweet(h.Client, trigger.InReplyToID, h.Opts)
		if err != nil {
			// Finding #2: Sanitize InReplyToID in log output
			h.Logger.Printf("[WARN] Could not fetch parent tweet %s: %v (using trigger text only)", truncate(trigger.InReplyToID, 30), err)
			bugText = trigger.BugDescription
			mediaURLs = trigger.MediaURLs
		} else {
			bugText = parent.Text
			bugAuthor = parent.AuthorUsername
			mediaURLs = parent.MediaURLs
			// Also include trigger tweet media if any
			mediaURLs = append(mediaURLs, trigger.MediaURLs...)
		}
	} else {
		// Direct tweet (not a reply) — use the trigger text itself
		bugText = trigger.BugDescription
		mediaURLs = trigger.MediaURLs
	}

	h.Logger.Printf("[BUG] %s", truncate(bugText, 200))
	if len(mediaURLs) > 0 {
		h.Logger.Printf("[MEDIA] %d attachment(s) found", len(mediaURLs))
	}

	// 2. Download media to temp dir
	var mediaFiles []string
	if len(mediaURLs) > 0 {
		var err error
		mediaFiles, err = downloadMedia(mediaURLs)
		if err != nil {
			h.Logger.Printf("[WARN] Media download failed: %v (continuing without media)", err)
		} else {
			defer cleanupMedia(mediaFiles)
		}
	}

	// 3. Generate branch name (M6: tweet ID validated as numeric above)
	branchName := h.Config.BranchPrefix + trigger.ID

	// 4. Dry run check
	if h.Config.DryRun {
		h.Logger.Printf("[DRY-RUN] Would run %q agent for bug: %s", h.Config.Agent, truncate(bugText, 100))
		h.Logger.Printf("[DRY-RUN] Branch: %s, Repo: %s", branchName, h.Config.Repo)
		h.State.MarkProcessed(trigger.ID, "skipped")
		return h.State.Save()
	}

	// 5. Run the coding agent
	// Finding #3: Record rate limit slot only when agent actually runs (not for dry-run/early-exit)
	h.recordAgentRun()
	h.Logger.Printf("[AGENT] Running %s agent...", h.Agent.Name())

	founderNote := trigger.BugDescription
	prompt := bugText
	// Finding #6: Validate bugAuthor before including in prompt
	if bugAuthor != "" && validHandle.MatchString(bugAuthor) {
		prompt = fmt.Sprintf("Bug from @%s: %s", bugAuthor, bugText)
	}

	result, err := h.Agent.Run(ctx, prompt, founderNote, mediaFiles, h.Config.Repo, branchName)
	if err != nil {
		h.Logger.Printf("[ERROR] Agent failed: %v", err)
		if result != nil && result.Output != "" {
			h.Logger.Printf("[AGENT OUTPUT] %s", truncate(result.Output, 500))
		}
		h.State.MarkProcessed(trigger.ID, "failed")
		_ = h.State.Save()
		return fmt.Errorf("agent failed: %w", err)
	}

	if result.PRLink != "" {
		h.Logger.Printf("[DONE] PR created: %s", result.PRLink)
	} else {
		h.Logger.Printf("[WARN] Agent completed but no PR link found")
	}

	// 6. Update state
	status := "success"
	if result.PRLink == "" {
		status = "no_pr"
	}
	h.State.MarkProcessed(trigger.ID, status)
	return h.State.Save()
}

// safeHTTPClient creates an HTTP client with SSRF protections.
// H1: Custom DialContext validates resolved IPs at connection time (prevents DNS rebinding TOCTOU).
// H1: CheckRedirect validates redirect URLs against the allowlist.
// M3: Explicit timeout prevents hanging on slow servers.
func safeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}

			// H1: Resolve and validate IP at connection time (not before)
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed for %s: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP addresses found for %s", host)
			}

			for _, ipAddr := range ips {
				ip := ipAddr.IP
				// Issue #5: Normalize IPv6-mapped IPv4 addresses before validation
				if ip4 := ip.To4(); ip4 != nil {
					ip = ip4
				}
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
					return nil, fmt.Errorf("blocked private/loopback IP %s for host %s", ip, host)
				}
			}

			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			// H1: Validate every redirect URL against the allowlist
			if err := validateMediaURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// validateMediaURL checks that a URL is safe to download.
// C2: Prevents SSRF by restricting to HTTPS and known media hosts.
// H1: DNS/IP validation is now handled by safeHTTPClient's DialContext (no TOCTOU).
func validateMediaURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// M5: HTTPS only
	if parsed.Scheme != "https" {
		return fmt.Errorf("non-HTTPS URL rejected: %s", parsed.Scheme)
	}

	host := parsed.Hostname()

	// C2: Only allow known X media hosts
	if !allowedMediaHosts[host] {
		return fmt.Errorf("blocked media host: %s (allowed: twimg.com domains)", host)
	}

	return nil
}

// downloadMedia downloads media from URLs to a temp directory.
func downloadMedia(urls []string) ([]string, error) {
	// M4: Cap number of media URLs
	if len(urls) > maxMediaURLs {
		urls = urls[:maxMediaURLs]
	}

	tmpDir, err := os.MkdirTemp("", "xbot-media-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}

	// H1/M3: Use safe HTTP client with SSRF protection and timeouts
	client := safeHTTPClient()

	var files []string
	for i, u := range urls {
		// C2+M5: Validate URL before downloading
		if err := validateMediaURL(u); err != nil {
			continue // Skip invalid URLs
		}

		// M5: Parse URL properly, extract extension from path only
		ext := safeMediaExtension(u)

		filePath := filepath.Join(tmpDir, fmt.Sprintf("media_%d%s", i, ext))

		resp, err := client.Get(u)
		if err != nil {
			continue // Skip failed downloads
		}

		f, err := os.Create(filePath)
		if err != nil {
			resp.Body.Close()
			continue
		}

		// H1: Limit download size to prevent memory/disk exhaustion
		limited := io.LimitReader(resp.Body, maxMediaFileSize+1)
		n, err := io.Copy(f, limited)
		resp.Body.Close()
		f.Close()

		if n > maxMediaFileSize {
			os.Remove(filePath)
			continue // Skip oversized files
		}

		if err == nil {
			files = append(files, filePath)
		}
	}

	if len(files) == 0 && len(urls) > 0 {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to download any media")
	}

	return files, nil
}

// safeMediaExtension extracts a safe file extension from a media URL.
// M5: Parses URL properly, only uses path component, validates against allowlist.
func safeMediaExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ".jpg"
	}

	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if allowedMediaExtensions[ext] {
		return ext
	}
	return ".jpg" // Safe default
}

// cleanupMedia removes downloaded media files.
func cleanupMedia(files []string) {
	if len(files) == 0 {
		return
	}
	// All files are in the same temp dir
	dir := filepath.Dir(files[0])
	os.RemoveAll(dir)
}

// truncate shortens a string for log output and strips control characters (L3).
// L7: Uses rune-based length to avoid splitting multi-byte UTF-8 characters.
func truncate(s string, maxLen int) string {
	// L3: Strip control characters to prevent log injection/terminal escape sequences
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1 // Drop other control chars
		}
		return r
	}, s)

	// L7: Count runes, not bytes
	if utf8.RuneCountInString(s) > maxLen {
		runes := []rune(s)
		return string(runes[:maxLen]) + "..."
	}
	return s
}
