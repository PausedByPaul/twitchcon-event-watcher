package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ── API types ──────────────────────────────────────────────────────────────────

type Speaker struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	URL   string `json:"url"`
}

type SessionData struct {
	Date           string    `json:"date"`
	Featured       bool      `json:"featured"`
	Private        bool      `json:"private"`
	Description    string    `json:"description"`
	Program        string    `json:"program"`
	Title          string    `json:"title"`
	URL            string    `json:"url"`
	Tags           []string  `json:"tags"`
	Speakers       []Speaker `json:"speakers"`
	Location       string    `json:"location"`
	Time           string    `json:"time"`
	EndTimestamp   string    `json:"endTimestamp"`
	MobileOnly     bool      `json:"mobileOnly"`
	StartTimestamp string    `json:"startTimestamp"`
}

type Session struct {
	EventName string      `json:"eventName"`
	Data      SessionData `json:"data"`
	SessionID string      `json:"sessionId"`
}

type APIResponse struct {
	Sessions []Session `json:"sessions"`
}

// ── Exhibitor API types ────────────────────────────────────────────────────────

type ExhibitorData struct {
	Image       string   `json:"image"`
	Name        string   `json:"name"`
	Link        string   `json:"link"`
	Description string   `json:"description"`
	VendorType  string   `json:"vendor_type"`
	Booth       string   `json:"booth"`
	Tags        []string `json:"tags"`
}

type Exhibitor struct {
	EventName   string        `json:"eventName"`
	Data        ExhibitorData `json:"data"`
	ExhibitorID string        `json:"exhibitorId"`
}

type ExhibitorAPIResponse struct {
	Exhibitors []Exhibitor `json:"exhibitors"`
}

// ── Discord types ──────────────────────────────────────────────────────────────

type DiscordEmbedFooter struct {
	Text string `json:"text"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Fields      []DiscordEmbedField `json:"fields,omitempty"`
	Footer      *DiscordEmbedFooter `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

type DiscordWebhookPayload struct {
	Username  string         `json:"username,omitempty"`
	AvatarURL string         `json:"avatar_url,omitempty"`
	Content   string         `json:"content,omitempty"`
	Embeds    []DiscordEmbed `json:"embeds,omitempty"`
}

// ── Change types ───────────────────────────────────────────────────────────────

type SessionChange struct {
	Old Session
	New Session
}

type Changes struct {
	Added   []Session
	Removed []Session
	Updated []SessionChange
}

type ExhibitorChange struct {
	Old Exhibitor
	New Exhibitor
}

type ExhibitorChanges struct {
	Added   []Exhibitor
	Removed []Exhibitor
	Updated []ExhibitorChange
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func normalizeTag(s string) string {
	s = strings.ReplaceAll(s, `\u0026`, "&")
	s = strings.TrimSpace(s)
	return s
}

func speakerNames(speakers []Speaker) string {
	if len(speakers) == 0 {
		return "TBA"
	}
	names := make([]string, len(speakers))
	for i, s := range speakers {
		names[i] = s.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func sortedTags(tags []string) string {
	cp := make([]string, len(tags))
	for i, t := range tags {
		cp[i] = normalizeTag(t)
	}
	sort.Strings(cp)
	return strings.Join(cp, ", ")
}

// sessionKey hashes the fields we care about for change detection.
// We use a deterministic serialisation so ordering differences don't count.
func sessionKey(s Session) string {
	d := s.Data
	// Normalise slice ordering before serialising.
	tagStr := sortedTags(d.Tags)
	speakerStr := speakerNames(d.Speakers)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%v|%v",
		d.Title, d.Date, d.Time, d.Location, d.Program,
		tagStr, speakerStr, d.Description,
		d.Featured, d.Private,
	)
}

// exhibitorKey hashes the fields we care about for exhibitor change detection.
func exhibitorKey(e Exhibitor) string {
	d := e.Data
	tagStr := sortedTags(d.Tags)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		d.Name, d.Booth, d.VendorType, d.Link, tagStr, d.Description,
	)
}

// inlineDiff produces a Discord-markdown string showing word-level changes
// between oldVal and newVal. Words removed are struck through (~~word~~);
// words added are bold (**word**); unchanged words are kept as-is.
// inlineDiff produces a Discord markdown string showing word-level changes
// between oldVal and newVal. Removed word runs are prefixed with 🔴 and struck
// through; added word runs are prefixed with 🟢 and bold. Both render on
// desktop and mobile without ANSI escape codes.
func inlineDiff(oldVal, newVal string) string {
	if oldVal == newVal {
		return newVal
	}
	oldWords := strings.Fields(oldVal)
	newWords := strings.Fields(newVal)
	if len(oldWords) == 0 {
		return "🟢 **" + newVal + "**"
	}
	if len(newWords) == 0 {
		return "🔴 ~~" + oldVal + "~~"
	}

	m, n := len(oldWords), len(newWords)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldWords[i-1] == newWords[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	type diffOp struct {
		kind int // 0=equal, 1=delete, 2=insert
		word string
	}
	ops := make([]diffOp, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldWords[i-1] == newWords[j-1]:
			ops = append(ops, diffOp{0, oldWords[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			ops = append(ops, diffOp{2, newWords[j-1]})
			j--
		default:
			ops = append(ops, diffOp{1, oldWords[i-1]})
			i--
		}
	}
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}

	var sb strings.Builder
	idx := 0
	for idx < len(ops) {
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		switch ops[idx].kind {
		case 0:
			sb.WriteString(ops[idx].word)
			idx++
		case 1:
			var run []string
			for idx < len(ops) && ops[idx].kind == 1 {
				run = append(run, ops[idx].word)
				idx++
			}
			sb.WriteString("🔴 ~~" + strings.Join(run, " ") + "~~")
		case 2:
			var run []string
			for idx < len(ops) && ops[idx].kind == 2 {
				run = append(run, ops[idx].word)
				idx++
			}
			sb.WriteString("🟢 **" + strings.Join(run, " ") + "**")
		}
	}
	return sb.String()
}

// ── API ────────────────────────────────────────────────────────────────────────

func fetchSessions(apiURL string) ([]Session, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var ar APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return ar.Sessions, nil
}

func fetchExhibitors(apiURL string) ([]Exhibitor, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var ar ExhibitorAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return ar.Exhibitors, nil
}

// ── State persistence ──────────────────────────────────────────────────────────

func loadState(path string) ([]Session, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func saveState(path string, sessions []Session) error {
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadExhibitorState(path string) ([]Exhibitor, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var exhibitors []Exhibitor
	if err := json.Unmarshal(data, &exhibitors); err != nil {
		return nil, err
	}
	return exhibitors, nil
}

func saveExhibitorState(path string, exhibitors []Exhibitor) error {
	data, err := json.MarshalIndent(exhibitors, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ── Change detection ───────────────────────────────────────────────────────────

func detectChanges(previous, current []Session) Changes {
	oldMap := make(map[string]Session, len(previous))
	for _, s := range previous {
		oldMap[s.SessionID] = s
	}
	newMap := make(map[string]Session, len(current))
	for _, s := range current {
		newMap[s.SessionID] = s
	}

	var changes Changes

	for id, ns := range newMap {
		os, exists := oldMap[id]
		if !exists {
			changes.Added = append(changes.Added, ns)
		} else if sessionKey(os) != sessionKey(ns) {
			changes.Updated = append(changes.Updated, SessionChange{Old: os, New: ns})
		}
	}

	for id, os := range oldMap {
		if _, exists := newMap[id]; !exists {
			changes.Removed = append(changes.Removed, os)
		}
	}

	byStart := func(a, b Session) bool { return a.Data.StartTimestamp < b.Data.StartTimestamp }
	sort.Slice(changes.Added, func(i, j int) bool { return byStart(changes.Added[i], changes.Added[j]) })
	sort.Slice(changes.Removed, func(i, j int) bool { return byStart(changes.Removed[i], changes.Removed[j]) })
	sort.Slice(changes.Updated, func(i, j int) bool {
		return changes.Updated[i].New.Data.StartTimestamp < changes.Updated[j].New.Data.StartTimestamp
	})

	return changes
}

func detectExhibitorChanges(previous, current []Exhibitor) ExhibitorChanges {
	oldMap := make(map[string]Exhibitor, len(previous))
	for _, e := range previous {
		oldMap[e.ExhibitorID] = e
	}
	newMap := make(map[string]Exhibitor, len(current))
	for _, e := range current {
		newMap[e.ExhibitorID] = e
	}

	var changes ExhibitorChanges

	for id, ne := range newMap {
		oe, exists := oldMap[id]
		if !exists {
			changes.Added = append(changes.Added, ne)
		} else if exhibitorKey(oe) != exhibitorKey(ne) {
			changes.Updated = append(changes.Updated, ExhibitorChange{Old: oe, New: ne})
		}
	}

	for id, oe := range oldMap {
		if _, exists := newMap[id]; !exists {
			changes.Removed = append(changes.Removed, oe)
		}
	}

	byName := func(a, b Exhibitor) bool { return a.Data.Name < b.Data.Name }
	sort.Slice(changes.Added, func(i, j int) bool { return byName(changes.Added[i], changes.Added[j]) })
	sort.Slice(changes.Removed, func(i, j int) bool { return byName(changes.Removed[i], changes.Removed[j]) })
	sort.Slice(changes.Updated, func(i, j int) bool {
		return changes.Updated[i].New.Data.Name < changes.Updated[j].New.Data.Name
	})

	return changes
}

// ── Discord payload builder ────────────────────────────────────────────────────

const (
	colorGreen  = 0x00C851
	colorRed    = 0xFF4444
	colorAmber  = 0xFFAA00
	maxEmbeds   = 10
	maxEmbedLen = 6000 // total characters per message across all embeds
)

func diffFields(old, new Session) []DiscordEmbedField {
	var fields []DiscordEmbedField
	add := func(name, oldVal, newVal string) {
		if oldVal != newVal {
			fields = append(fields, DiscordEmbedField{
				Name:  name,
				Value: truncate(inlineDiff(oldVal, newVal), 1024),
			})
		}
	}

	add("Title", old.Data.Title, new.Data.Title)
	add("Date", old.Data.Date, new.Data.Date)
	add("Time", old.Data.Time, new.Data.Time)
	add("Location", old.Data.Location, new.Data.Location)
	add("Program", old.Data.Program, new.Data.Program)
	add("Speakers", speakerNames(old.Data.Speakers), speakerNames(new.Data.Speakers))
	add("Tags", sortedTags(old.Data.Tags), sortedTags(new.Data.Tags))
	add("Featured", fmt.Sprintf("%v", old.Data.Featured), fmt.Sprintf("%v", new.Data.Featured))
	add("Private", fmt.Sprintf("%v", old.Data.Private), fmt.Sprintf("%v", new.Data.Private))

	if old.Data.Description != new.Data.Description {
		fields = append(fields, DiscordEmbedField{
			Name:  "Description",
			Value: truncate(inlineDiff(old.Data.Description, new.Data.Description), 1024),
		})
	}

	return fields
}

func buildEmbeds(changes Changes, ts string) []DiscordEmbed {
	var embeds []DiscordEmbed

	for _, s := range changes.Added {
		speakers := speakerNames(s.Data.Speakers)
		desc := fmt.Sprintf("**%s** | %s\n📅 %s  %s\n📍 %s\n👥 %s",
			s.Data.Program,
			sortedTags(s.Data.Tags),
			s.Data.Date,
			s.Data.Time,
			s.Data.Location,
			speakers,
		)
		embeds = append(embeds, DiscordEmbed{
			Title:       truncate("➕ "+s.Data.Title, 256),
			Description: truncate(desc, 4096),
			Color:       colorGreen,
			Timestamp:   ts,
			Footer:      &DiscordEmbedFooter{Text: s.SessionID},
		})
	}

	for _, s := range changes.Removed {
		desc := fmt.Sprintf("**%s** | %s\n📅 %s  %s\n📍 %s",
			s.Data.Program,
			sortedTags(s.Data.Tags),
			s.Data.Date,
			s.Data.Time,
			s.Data.Location,
		)
		embeds = append(embeds, DiscordEmbed{
			Title:       truncate("❌ "+s.Data.Title, 256),
			Description: truncate(desc, 4096),
			Color:       colorRed,
			Timestamp:   ts,
			Footer:      &DiscordEmbedFooter{Text: s.SessionID},
		})
	}

	for _, c := range changes.Updated {
		fields := diffFields(c.Old, c.New)
		if len(fields) == 0 {
			continue
		}
		embeds = append(embeds, DiscordEmbed{
			Title:     truncate("✏️ "+c.New.Data.Title, 256),
			Color:     colorAmber,
			Fields:    fields,
			Timestamp: ts,
			Footer:    &DiscordEmbedFooter{Text: c.New.SessionID},
		})
	}

	return embeds
}

// embedCharCount returns a rough character count for the embed (Discord limit: 6000/message).
func embedCharCount(e DiscordEmbed) int {
	n := len(e.Title) + len(e.Description)
	if e.Footer != nil {
		n += len(e.Footer.Text)
	}
	for _, f := range e.Fields {
		n += len(f.Name) + len(f.Value)
	}
	return n
}

func buildPayloads(changes Changes, webhookUsername string) []DiscordWebhookPayload {
	if len(changes.Added) == 0 && len(changes.Removed) == 0 && len(changes.Updated) == 0 {
		return nil
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	allEmbeds := buildEmbeds(changes, ts)

	summary := fmt.Sprintf(
		"**TwitchCon Rotterdam 2026 — schedule update detected**\n➕ %d added  ❌ %d removed  ✏️ %d updated",
		len(changes.Added), len(changes.Removed), len(changes.Updated),
	)

	var payloads []DiscordWebhookPayload
	var batch []DiscordEmbed
	batchChars := 0
	isFirst := true

	flush := func() {
		if len(batch) == 0 {
			return
		}
		p := DiscordWebhookPayload{
			Username: webhookUsername,
			Embeds:   batch,
		}
		if isFirst {
			p.Content = summary
			isFirst = false
		}
		payloads = append(payloads, p)
		batch = nil
		batchChars = 0
	}

	for _, e := range allEmbeds {
		ec := embedCharCount(e)
		if len(batch) == maxEmbeds || (batchChars+ec > maxEmbedLen && len(batch) > 0) {
			flush()
		}
		batch = append(batch, e)
		batchChars += ec
	}
	flush()

	// Edge case: no embeds were built (all diffs were empty) but changes existed.
	if len(payloads) == 0 && !isFirst {
		payloads = append(payloads, DiscordWebhookPayload{
			Username: webhookUsername,
			Content:  summary,
		})
	}

	return payloads
}

func diffExhibitorFields(old, new Exhibitor) []DiscordEmbedField {
	var fields []DiscordEmbedField
	add := func(name, oldVal, newVal string) {
		if oldVal != newVal {
			fields = append(fields, DiscordEmbedField{
				Name:  name,
				Value: truncate(inlineDiff(oldVal, newVal), 1024),
			})
		}
	}

	add("Name", old.Data.Name, new.Data.Name)
	add("Booth", old.Data.Booth, new.Data.Booth)
	add("Type", old.Data.VendorType, new.Data.VendorType)
	add("Link", old.Data.Link, new.Data.Link)
	add("Tags", sortedTags(old.Data.Tags), sortedTags(new.Data.Tags))

	if old.Data.Description != new.Data.Description {
		fields = append(fields, DiscordEmbedField{
			Name:  "Description",
			Value: truncate(inlineDiff(old.Data.Description, new.Data.Description), 1024),
		})
	}

	return fields
}

func buildExhibitorEmbeds(changes ExhibitorChanges, ts string) []DiscordEmbed {
	var embeds []DiscordEmbed

	for _, e := range changes.Added {
		desc := fmt.Sprintf("**%s** | Booth: %s\n🔗 %s\n🏷️ %s",
			e.Data.VendorType, e.Data.Booth, e.Data.Link, sortedTags(e.Data.Tags),
		)
		embeds = append(embeds, DiscordEmbed{
			Title:       truncate("➕ "+e.Data.Name, 256),
			Description: truncate(desc, 4096),
			Color:       colorGreen,
			Timestamp:   ts,
			Footer:      &DiscordEmbedFooter{Text: e.ExhibitorID},
		})
	}

	for _, e := range changes.Removed {
		desc := fmt.Sprintf("**%s** | Booth: %s", e.Data.VendorType, e.Data.Booth)
		embeds = append(embeds, DiscordEmbed{
			Title:       truncate("❌ "+e.Data.Name, 256),
			Description: truncate(desc, 4096),
			Color:       colorRed,
			Timestamp:   ts,
			Footer:      &DiscordEmbedFooter{Text: e.ExhibitorID},
		})
	}

	for _, c := range changes.Updated {
		fields := diffExhibitorFields(c.Old, c.New)
		if len(fields) == 0 {
			continue
		}
		embeds = append(embeds, DiscordEmbed{
			Title:     truncate("✏️ "+c.New.Data.Name, 256),
			Color:     colorAmber,
			Fields:    fields,
			Timestamp: ts,
			Footer:    &DiscordEmbedFooter{Text: c.New.ExhibitorID},
		})
	}

	return embeds
}

func buildExhibitorPayloads(changes ExhibitorChanges, webhookUsername string) []DiscordWebhookPayload {
	if len(changes.Added) == 0 && len(changes.Removed) == 0 && len(changes.Updated) == 0 {
		return nil
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	allEmbeds := buildExhibitorEmbeds(changes, ts)

	summary := fmt.Sprintf(
		"**TwitchCon Rotterdam 2026 — exhibitor update detected**\n➕ %d added  ❌ %d removed  ✏️ %d updated",
		len(changes.Added), len(changes.Removed), len(changes.Updated),
	)

	var payloads []DiscordWebhookPayload
	var batch []DiscordEmbed
	batchChars := 0
	isFirst := true

	flush := func() {
		if len(batch) == 0 {
			return
		}
		p := DiscordWebhookPayload{
			Username: webhookUsername,
			Embeds:   batch,
		}
		if isFirst {
			p.Content = summary
			isFirst = false
		}
		payloads = append(payloads, p)
		batch = nil
		batchChars = 0
	}

	for _, e := range allEmbeds {
		ec := embedCharCount(e)
		if len(batch) == maxEmbeds || (batchChars+ec > maxEmbedLen && len(batch) > 0) {
			flush()
		}
		batch = append(batch, e)
		batchChars += ec
	}
	flush()

	if len(payloads) == 0 && !isFirst {
		payloads = append(payloads, DiscordWebhookPayload{
			Username: webhookUsername,
			Content:  summary,
		})
	}

	return payloads
}

// ── Webhook sender ─────────────────────────────────────────────────────────────

func sendWebhook(webhookURL string, payload DiscordWebhookPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(webhookURL, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ── Poll ───────────────────────────────────────────────────────────────────────

func poll(cfg config) {
	log.Println("Polling TwitchCon sessions API…")

	current, err := fetchSessions(cfg.apiURL)
	if err != nil {
		log.Printf("ERROR fetching sessions: %v", err)
		return
	}
	log.Printf("Fetched %d sessions", len(current))

	previous, err := loadState(cfg.stateFile)
	if err != nil {
		log.Printf("ERROR loading state: %v", err)
		return
	}

	if previous == nil {
		log.Println("No previous state found — saving initial snapshot (no notifications sent)")
		if err := saveState(cfg.stateFile, current); err != nil {
			log.Printf("ERROR saving state: %v", err)
		}
	} else {
		changes := detectChanges(previous, current)
		log.Printf("Session changes: %d added, %d removed, %d updated",
			len(changes.Added), len(changes.Removed), len(changes.Updated))

		if len(changes.Added)+len(changes.Removed)+len(changes.Updated) > 0 {
			payloads := buildPayloads(changes, cfg.webhookUsername)
			for i, p := range payloads {
				if err := sendWebhook(cfg.webhookURL, p); err != nil {
					log.Printf("ERROR sending webhook payload %d/%d: %v", i+1, len(payloads), err)
				} else {
					log.Printf("Sent webhook payload %d/%d", i+1, len(payloads))
				}
				// Respect Discord's rate limit (≈5 req/2 s for webhooks).
				if i < len(payloads)-1 {
					time.Sleep(500 * time.Millisecond)
				}
			}
		}

		if err := saveState(cfg.stateFile, current); err != nil {
			log.Printf("ERROR saving state: %v", err)
		}
	}

	// ── Exhibitors ──────────────────────────────────────────────────────────────

	log.Println("Polling TwitchCon exhibitors API…")

	currentExhibitors, err := fetchExhibitors(cfg.exhibitorsAPIURL)
	if err != nil {
		log.Printf("ERROR fetching exhibitors: %v", err)
		return
	}
	log.Printf("Fetched %d exhibitors", len(currentExhibitors))

	previousExhibitors, err := loadExhibitorState(cfg.exhibitorsStateFile)
	if err != nil {
		log.Printf("ERROR loading exhibitor state: %v", err)
		return
	}

	if previousExhibitors == nil {
		log.Println("No previous exhibitor state found — saving initial snapshot (no notifications sent)")
		if err := saveExhibitorState(cfg.exhibitorsStateFile, currentExhibitors); err != nil {
			log.Printf("ERROR saving exhibitor state: %v", err)
		}
		return
	}

	exhibitorChanges := detectExhibitorChanges(previousExhibitors, currentExhibitors)
	log.Printf("Exhibitor changes: %d added, %d removed, %d updated",
		len(exhibitorChanges.Added), len(exhibitorChanges.Removed), len(exhibitorChanges.Updated))

	if len(exhibitorChanges.Added)+len(exhibitorChanges.Removed)+len(exhibitorChanges.Updated) > 0 {
		payloads := buildExhibitorPayloads(exhibitorChanges, cfg.webhookUsername)
		for i, p := range payloads {
			if err := sendWebhook(cfg.webhookURL, p); err != nil {
				log.Printf("ERROR sending exhibitor webhook payload %d/%d: %v", i+1, len(payloads), err)
			} else {
				log.Printf("Sent exhibitor webhook payload %d/%d", i+1, len(payloads))
			}
			if i < len(payloads)-1 {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}

	if err := saveExhibitorState(cfg.exhibitorsStateFile, currentExhibitors); err != nil {
		log.Printf("ERROR saving exhibitor state: %v", err)
	}
}

// ── Config & main ──────────────────────────────────────────────────────────────

type config struct {
	webhookURL          string
	webhookUsername     string
	apiURL              string
	stateFile           string
	exhibitorsAPIURL    string
	exhibitorsStateFile string
	pollInterval        time.Duration
}

func loadConfig() (config, error) {
	webhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if webhookURL == "" {
		return config{}, fmt.Errorf("DISCORD_WEBHOOK_URL environment variable is required")
	}

	cfg := config{
		webhookURL:          webhookURL,
		webhookUsername:     envOr("WEBHOOK_USERNAME", "TwitchCon Watcher"),
		apiURL:              envOr("API_URL", "https://api.twitchcon.com/sessions?eventName=rotterdam-2026"),
		stateFile:           envOr("STATE_FILE", "sessions_state.json"),
		exhibitorsAPIURL:    envOr("EXHIBITORS_API_URL", "https://api.twitchcon.com/exhibitors?eventName=rotterdam-2026"),
		exhibitorsStateFile: envOr("EXHIBITORS_STATE_FILE", "exhibitors_state.json"),
		pollInterval:        time.Hour,
	}

	if s := os.Getenv("POLL_INTERVAL"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return config{}, fmt.Errorf("invalid POLL_INTERVAL %q: %w", s, err)
		}
		cfg.pollInterval = d
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	log.Printf("TwitchCon event watcher starting (poll interval: %s, state file: %s)",
		cfg.pollInterval, cfg.stateFile)

	// Run immediately on startup.
	poll(cfg)

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		poll(cfg)
	}
}
