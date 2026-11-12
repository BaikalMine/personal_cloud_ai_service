package gateway

import (
	"net/url"
	"strconv"
	"testing"
)

func TestPostprocessReferencesPaginationIntegration(t *testing.T) {
	p := newPostprocessConfirmationFixture(t)
	source := p.quote.Sources[0].Identity
	const first int64 = 9007199254741000
	_, err := p.f.db.Exec(`INSERT INTO content_media(id,event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash)
	 SELECT $2::bigint+n,event_id,media_type,mime_type,'reference_%_'||n,payload_cipher,size_bytes,content_hash
	 FROM content_media CROSS JOIN generate_series(0,54) n WHERE id=$1`, source.MediaID, first)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"exclude": {strconv.FormatInt(source.MediaID, 10)}, "search": {"_%_"}}
	seen := map[string]bool{}
	for page := 0; page < 2; page++ {
		result := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", values), 200)
		items := result["items"].([]any)
		want := 50
		if page == 1 {
			want = 5
		}
		if len(items) != want {
			t.Fatal("wrong page size", result)
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			id, ok := item["media_id"].(string)
			if !ok || seen[id] || item["job_id"] != source.JobPublicID || item["url"] != "/generate/library/"+id || item["sensitive"] != true {
				t.Fatal("identity, sensitivity or duplication", item)
			}
			seen[id] = true
		}
		next := result["next"].(string)
		if page == 0 && next != strconv.FormatInt(first+5, 10) || page == 1 && next != "" {
			t.Fatal("cursor", result)
		}
		values.Set("before", next)
	}
	for n := int64(0); n < 55; n++ {
		if !seen[strconv.FormatInt(first+n, 10)] {
			t.Fatal("lost exact large ID", n)
		}
	}
	values.Set("search", "not-present%")
	result := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", values), 200)
	if len(result["items"].([]any)) != 0 {
		t.Fatal("search treated as wildcard", result)
	}
	for _, value := range []string{"01", "0", "-1", "1e3", "9223372036854775808"} {
		postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", url.Values{"before": {value}}), 422)
	}
}

func TestPostprocessReferencesEligibilityIntegration(t *testing.T) {
	for name, query := range map[string]string{
		"hidden":        `UPDATE content_media SET profile_hidden_at=now()`,
		"expired media": `UPDATE content_media SET expires_at=now()-interval '1 second'`,
		"expired event": `UPDATE content_events SET expires_at=now()-interval '1 second'`,
		"unfinished":    `UPDATE generation_jobs SET state='running'`,
		"unsupported":   `UPDATE content_media SET mime_type='image/gif'`,
		"empty":         `UPDATE content_media SET size_bytes=0`,
		"too large":     `UPDATE content_media SET size_bytes=536870913`,
		"family denied": `UPDATE users SET role='user',can_generate_text_to_image=true,can_generate_video=false; UPDATE generation_jobs SET template_id='minimax-h3-video'`,
		"deleted job":   `DELETE FROM generation_jobs`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPostprocessConfirmationFixture(t)
			if _, err := p.f.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			result := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", nil), 200)
			if len(result["items"].([]any)) != 0 || result["next"] != "" {
				t.Fatal("ineligible reference exposed", result)
			}
		})
	}
	t.Run("owner", func(t *testing.T) {
		p := newPostprocessConfirmationFixture(t)
		var otherID int64
		if err := p.f.db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation) VALUES('reference-outsider','disabled','admin',true) RETURNING id`).Scan(&otherID); err != nil {
			t.Fatal(err)
		}
		other, err := p.f.app.store.UserByID(t.Context(), otherID)
		if err != nil {
			t.Fatal(err)
		}
		original := p.f.user
		p.f.user = other
		result := postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", nil), 200)
		if len(result["items"].([]any)) != 0 {
			t.Fatal("foreign reference exposed", result)
		}
		p.f.user = original
		if _, err := p.f.db.Exec(`UPDATE users SET disabled=true`); err != nil {
			t.Fatal(err)
		}
		postprocessHTTPResult(t, p.call("GET", "/generate/postprocess/references", nil), 403)
	})
}
