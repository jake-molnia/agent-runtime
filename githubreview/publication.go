package githubreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

const maxBodyBytes = 60000
const legacyMarker = "<!-- homelab-pr-review:v1 -->"

func completionMarker(key string) string { return "<!-- agent-runtime-review:" + key + " -->" }
func hasHeaderMarker(body, marker string) bool {
	header, _, _ := strings.Cut(body, "\n\n")
	for _, line := range strings.Split(header, "\n") {
		if line == marker {
			return true
		}
	}
	return false
}
func completedReview(reviews []Review, input Input, key, digest string) int64 {
	old := sha256.Sum256([]byte(fmt.Sprintf("%d/%d/%s/%s", input.RepositoryID, input.Number, input.HeadSHA, digest)))
	for _, review := range reviews {
		if review.ID <= 0 || review.CommitID != input.HeadSHA {
			continue
		}
		if strings.HasSuffix(review.Body, "\n\n"+completionMarker(key)) || strings.HasSuffix(review.Body, "\n\n"+completionMarker(hex.EncodeToString(old[:]))) || (strings.HasPrefix(review.Body, legacyMarker+"\n") && !strings.Contains(review.Body, "<!-- agent-runtime-part:") && hasHeaderMarker(review.Body, "<!-- head:"+input.HeadSHA+" -->")) {
			return review.ID
		}
	}
	return 0
}
func reviewRequests(input Input, key, summary string, findings []Finding, lines map[string]map[int]bool) []ReviewRequest {
	const limit = maxBodyBytes - 512
	bodies := []string{}
	appendText := func(text string) {
		for len(text) > 0 {
			if len(bodies) == 0 || len(bodies[len(bodies)-1]) == limit {
				bodies = append(bodies, "")
			}
			available := limit - len(bodies[len(bodies)-1])
			n := len(text)
			if n > available {
				n = available
				for n > 0 && !utf8.RuneStart(text[n]) {
					n--
				}
			}
			if n == 0 {
				bodies = append(bodies, "")
				continue
			}
			bodies[len(bodies)-1] += text[:n]
			text = text[n:]
		}
	}
	appendText(summary)
	comments := []ReviewComment{}
	for _, finding := range findings {
		if lines[finding.Path][finding.Line] {
			comments = append(comments, ReviewComment{Path: finding.Path, Line: finding.Line, Side: "RIGHT", Body: finding.Body})
			continue
		}
		segments := strings.Split(finding.Path, "/")
		for i := range segments {
			segments[i] = url.PathEscape(segments[i])
		}
		link := fmt.Sprintf("https://github.com/%s/blob/%s/%s#L%d", input.Repository, input.HeadSHA, strings.Join(segments, "/"), finding.Line)
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;").Replace(finding.Path)
		section := fmt.Sprintf("\n\n### [%s:%d](%s)\n\n%s", label, finding.Line, link, finding.Body)
		if len(bodies[len(bodies)-1])+len(section) > limit {
			bodies = append(bodies, "")
		}
		appendText(section)
	}
	requests := make([]ReviewRequest, len(bodies))
	for i, body := range bodies {
		requests[i] = ReviewRequest{CommitID: input.HeadSHA, Event: "COMMENT", Body: body}
		if i == 0 {
			requests[i].Comments = comments
		}
	}
	raw, _ := json.Marshal(requests)
	digest := sha256.Sum256(raw)
	for i := range requests {
		requests[i].Body += "\n\n" + fmt.Sprintf("<!-- agent-runtime-part:%x:%d -->", digest, i)
		if i == len(requests)-1 {
			requests[i].Body += "\n\n" + completionMarker(key)
		}
	}
	return requests
}
func (handler *Handler) Publish(ctx context.Context, resolved Resolved, output json.RawMessage) (Outcome, error) {
	outcome := Outcome{Status: "skipped"}
	if err := validateInput(resolved.Input); err != nil {
		return outcome, err
	}
	if resolved.Digest == "" || len(resolved.Digest) > 256 || resolved.Key != reviewKey(resolved.Input, resolved.Digest) {
		return outcome, errors.New("invalid resolved review key")
	}
	if resolved.Skip {
		return outcome, nil
	}
	err := handler.Store.WithLock(ctx, prKey(resolved.Input), func(store LockedStore) error {
		record, exists, err := store.Load(ctx, resolved.Key)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("review was not resolved")
		}
		if record.Status == "completed" {
			outcome = Outcome{Status: "duplicate", ReviewID: record.ReviewID}
			return nil
		}
		current, err := handler.Client.Canonical(ctx, resolved.Input)
		if err != nil {
			return err
		}
		if err = validateCanonical(resolved.Input, current, false); err != nil {
			return err
		}
		if current.HeadSHA != resolved.Input.HeadSHA || current.Draft || current.State == "closed" {
			outcome.Status = "stale"
			return nil
		}
		reviews, err := handler.Client.Reviews(ctx, resolved.Input)
		if err != nil {
			return err
		}
		if id := completedReview(reviews, resolved.Input, resolved.Key, resolved.Digest); id > 0 {
			record.Status, record.ReviewID = "completed", id
			if err = store.Save(ctx, resolved.Key, record); err != nil {
				return err
			}
			outcome = Outcome{Status: "duplicate", ReviewID: id}
			return nil
		}
		if record.Status == "publishing" {
			return errors.New("publication outcome uncertain; retry reconciliation without reposting")
		}
		if len(record.Requests) == 0 {
			if err = ValidateResolvedOutput(resolved, output); err != nil {
				return err
			}
			files, err := handler.Client.Files(ctx, resolved.Input)
			if err != nil {
				return err
			}
			lines := map[string]map[int]bool{}
			// GitHub can omit or truncate patches. Those findings use linked body sections.
			for _, file := range files {
				if parsed, e := changedLines([]File{file}); e == nil {
					lines[file.Path] = parsed[file.Path]
				}
			}
			summary, findings, err := validateOutput(output)
			if err != nil {
				return err
			}
			record.Requests = reviewRequests(resolved.Input, resolved.Key, summary, findings, lines)
			if err = store.Save(ctx, resolved.Key, record); err != nil {
				return err
			}
		}
		for index, request := range record.Requests {
			partKey := fmt.Sprintf("%s/part/%d", resolved.Key, index)
			part, _, err := store.Load(ctx, partKey)
			if err != nil {
				return err
			}
			if part.Status == "completed" {
				record.ReviewID = part.ReviewID
				continue
			}
			// Exact payload matching excludes markers copied into model-authored prose.
			for _, review := range reviews {
				if review.ID > 0 && review.CommitID == request.CommitID && review.Body == request.Body {
					part.Status, part.ReviewID = "completed", review.ID
					break
				}
			}
			if part.Status == "completed" {
				if err = store.Save(ctx, partKey, part); err != nil {
					return err
				}
				record.ReviewID = part.ReviewID
				continue
			}
			if part.Status == "publishing" {
				return fmt.Errorf("publication outcome uncertain for part %d; retry reconciliation without reposting", index)
			}
			current, err = handler.Client.Canonical(ctx, resolved.Input)
			if err != nil {
				return err
			}
			if err = validateCanonical(resolved.Input, current, false); err != nil {
				return err
			}
			if current.HeadSHA != resolved.Input.HeadSHA || current.Draft || current.State == "closed" {
				outcome.Status = "stale"
				return nil
			}
			part = Record{Owner: record.Owner, Status: "publishing"}
			if err = store.Save(ctx, partKey, part); err != nil {
				return err
			}
			id, err := handler.Client.CreateReview(ctx, resolved.Input, request)
			if err != nil {
				var rejected *RejectedError
				var notPublished *notPublishedError
				if errors.As(err, &rejected) || errors.As(err, &notPublished) {
					part.Status = "resolved"
					if e := store.Save(ctx, partKey, part); e != nil {
						return e
					}
				}
				return err
			}
			if id <= 0 {
				return errors.New("GitHub returned an invalid review ID")
			}
			part.Status, part.ReviewID = "completed", id
			if err = store.Save(ctx, partKey, part); err != nil {
				return err
			}
			record.ReviewID = id
		}
		record.Status = "completed"
		if err = store.Save(ctx, resolved.Key, record); err != nil {
			return err
		}
		outcome = Outcome{Status: "published", ReviewID: record.ReviewID}
		return nil
	})
	return outcome, err
}
