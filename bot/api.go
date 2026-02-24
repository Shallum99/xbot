package bot

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/xdevplatform/xurl/api"
)

// sinceIDRegex validates that since_id is purely numeric.
var sinceIDRegex = regexp.MustCompile(`^[0-9]+$`)

// SearchTriggerTweets searches recent tweets with since_id for polling.
// Includes media expansions and reply fields needed by the bot.
func SearchTriggerTweets(client api.Client, query string, sinceID string, maxResults int, opts api.RequestOptions) (json.RawMessage, error) {
	q := url.QueryEscape(query)

	if maxResults < 10 {
		maxResults = 10
	} else if maxResults > 100 {
		maxResults = 100
	}

	endpoint := fmt.Sprintf(
		"/2/tweets/search/recent?query=%s&max_results=%d"+
			"&tweet.fields=created_at,public_metrics,conversation_id,in_reply_to_user_id,referenced_tweets,entities,attachments"+
			"&expansions=author_id,attachments.media_keys,referenced_tweets.id"+
			"&media.fields=url,preview_image_url,type"+
			"&user.fields=username,name,verified",
		q, maxResults,
	)
	// Validate since_id is numeric before appending to URL
	if sinceID != "" && sinceIDRegex.MatchString(sinceID) {
		endpoint += "&since_id=" + sinceID
	}

	opts.Method = "GET"
	opts.Endpoint = endpoint
	opts.Data = ""

	return client.SendRequest(opts)
}

// FetchTweet fetches a single tweet with media expansions.
// M2: Validates that post ID is purely numeric to prevent path injection in API URL.
func FetchTweet(client api.Client, postID string, opts api.RequestOptions) (json.RawMessage, error) {
	postID = ResolvePostID(postID)

	if !sinceIDRegex.MatchString(postID) {
		return nil, fmt.Errorf("invalid post ID %q: must be numeric", postID)
	}

	opts.Method = "GET"
	opts.Endpoint = fmt.Sprintf(
		"/2/tweets/%s?tweet.fields=created_at,public_metrics,conversation_id,in_reply_to_user_id,referenced_tweets,entities,attachments"+
			"&expansions=author_id,attachments.media_keys,referenced_tweets.id"+
			"&media.fields=url,preview_image_url,type"+
			"&user.fields=username,name",
		postID,
	)
	opts.Data = ""

	return client.SendRequest(opts)
}

// ResolvePostID extracts a post ID from a full URL or returns the input as-is.
func ResolvePostID(input string) string {
	input = strings.TrimSpace(input)

	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		parsed, err := url.Parse(input)
		if err == nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			for i, p := range parts {
				if p == "status" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return input
}
