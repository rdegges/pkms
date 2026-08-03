package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("frontmatter-schema", func(cfg map[string]any) (any, error) {
		return fmSchema{warningTypes: cfgStrings(cfg, "warning_types")}, nil
	})
	lint.Register("person-topics-kebab-case", func(cfg map[string]any) (any, error) {
		return topicsKebab{}, nil
	})
	lint.Register("meeting-attendees-are-wikilinks", func(cfg map[string]any) (any, error) {
		return attendeesWikilinks{}, nil
	})
	lint.Register("meeting-date-matches-path", func(cfg map[string]any) (any, error) {
		return dateMatchesPath{}, nil
	})
	lint.Register("meeting-time-matches-filename", func(cfg map[string]any) (any, error) {
		return timeMatchesFilename{}, nil
	})
	lint.Register("meeting-tags-domain", func(cfg map[string]any) (any, error) {
		return tagsDomain{}, nil
	})
	lint.Register("project-category-matches-path", func(cfg map[string]any) (any, error) {
		return categoryMatchesPath{}, nil
	})
	lint.Register("project-status-vocab", func(cfg map[string]any) (any, error) {
		allow := cfgStrings(cfg, "allowlist")
		if len(allow) == 0 {
			return nil, nil // unconfigured -> rule not applicable
		}
		return statusVocab{allow: allow}, nil
	})
}

// hasParsedFM gates rules that need well-formed frontmatter (missing /
// unclosed / invalid YAML are other rules' findings).
func hasParsedFM(n *vault.Note) bool {
	return n.FM != nil && !n.FM.Unclosed && n.FM.ParseErr == nil && n.FM.Fields != nil
}

// ---- frontmatter-schema ---------------------------------------------------

type fmSchema struct {
	warningTypes []string
}

var errPrinter = message.NewPrinter(language.English)

func (r fmSchema) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if !hasParsedFM(n) {
		return nil
	}
	typ := typedNote(ctx, n)
	sch := ctx.Prof.Schema(typ)
	if sch == nil {
		return nil
	}
	err := sch.Validate(n.FM.Fields)
	if err == nil {
		return nil
	}
	verr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []lint.Finding{finding(lint.Error, n.RelPath, 0, false,
			"%s schema: %v", typ, err)}
	}
	sev := lint.Error
	if contains(r.warningTypes, typ) {
		sev = lint.Warning
	}
	var out []lint.Finding
	for _, leaf := range leaves(verr) {
		line := 0
		loc := strings.Join(leaf.InstanceLocation, "/")
		if len(leaf.InstanceLocation) > 0 {
			line = n.FM.Lines[leaf.InstanceLocation[0]]
		}
		msg := leaf.ErrorKind.LocalizedString(errPrinter)
		if loc == "" {
			out = append(out, finding(sev, n.RelPath, line, false, "%s schema: %s", typ, msg))
		} else {
			out = append(out, finding(sev, n.RelPath, line, false, "%s schema: %s: %s", typ, loc, msg))
		}
	}
	return out
}

func leaves(e *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(e.Causes) == 0 {
		return []*jsonschema.ValidationError{e}
	}
	var out []*jsonschema.ValidationError
	for _, c := range e.Causes {
		out = append(out, leaves(c)...)
	}
	return out
}

// ---- person-topics-kebab-case ----------------------------------------------

var kebabRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type topicsKebab struct{}

func (topicsKebab) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "person" || !hasParsedFM(n) {
		return nil
	}
	topics, _, ok := n.FM.StringList("topics")
	if !ok {
		return nil
	}
	var out []lint.Finding
	for _, tp := range topics {
		if !kebabRe.MatchString(tp) {
			out = append(out, finding(lint.Warning, n.RelPath, n.FM.Lines["topics"], true,
				"topic %q is not kebab-case", tp))
		}
	}
	return out
}

func kebabize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[ _]+`).ReplaceAllString(s, "-")
	s = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

var topicMsgRe = regexp.MustCompile(`^topic "(.+)" is not kebab-case$`)

func (topicsKebab) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := topicMsgRe.FindStringSubmatch(f.Message)
	if m == nil {
		return nil, nil
	}
	old, fixed := m[1], kebabize(m[1])
	if fixed == "" || fixed == old {
		return nil, nil
	}
	// Replace only where the topic actually lives: its own list-item line
	// or the topics: flow line — never a coincidental match elsewhere.
	itemRe := regexp.MustCompile(`^(\s*-\s*["']?)` + regexp.QuoteMeta(old) + `(["']?\s*)$`)
	flowRe := regexp.MustCompile(`^\s*topics:.*` + regexp.QuoteMeta(old))
	lines := srcLines(n.Src)
	end := n.FM.EndLine
	for i := 1; i < end && i < len(lines); i++ {
		line := bytes.TrimSuffix(lines[i], []byte("\n"))
		if itemRe.Match(line) || flowRe.Match(line) {
			lines[i] = bytes.Replace(lines[i], []byte(old), []byte(fixed), 1)
			return &lint.FixResult{NewSrc: joinLines(lines)}, nil
		}
	}
	return nil, nil
}

// ---- meeting-attendees-are-wikilinks -----------------------------------------

type attendeesWikilinks struct{}

func (attendeesWikilinks) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "meeting" || !hasParsedFM(n) {
		return nil
	}
	attendees, _, ok := n.FM.StringList("attendees")
	if !ok {
		return nil
	}
	var out []lint.Finding
	for _, a := range attendees {
		target, _, alias, _, isLink := vault.ParseWikilinkString(a)
		switch {
		case !isLink:
			out = append(out, finding(lint.Error, n.RelPath, n.FM.Lines["attendees"], true,
				"attendee %q is not a [[wikilink]]", a))
		case alias != "" || target == "":
			out = append(out, finding(lint.Error, n.RelPath, n.FM.Lines["attendees"], false,
				"attendee %q must be a plain [[Full Name]] link", a))
		}
	}
	return out
}

var attendeeMsgRe = regexp.MustCompile(`^attendee "(.+)" is not a \[\[wikilink\]\]$`)

func (attendeesWikilinks) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := attendeeMsgRe.FindStringSubmatch(f.Message)
	if m == nil || strings.Contains(m[1], "[") {
		return nil, nil
	}
	bare := m[1]
	lines := srcLines(n.Src)
	end := n.FM.EndLine
	// Find the list item line `- Bare Name` (quoted or not) and wrap it.
	itemRe := regexp.MustCompile(`^(\s*-\s*)(["']?)` + regexp.QuoteMeta(bare) + `(["']?)\s*$`)
	for i := 1; i < end && i < len(lines); i++ {
		line := bytes.TrimSuffix(lines[i], []byte("\n"))
		if mm := itemRe.FindSubmatch(line); mm != nil {
			newLine := fmt.Sprintf(`%s"[[%s]]"`, mm[1], bare)
			out, ok := replaceLine(n.Src, i+1, []byte(newLine))
			if !ok {
				return nil, nil
			}
			return &lint.FixResult{NewSrc: out}, nil
		}
	}
	return nil, nil
}

// ---- meeting-date-matches-path (also covers daily-brief) ----------------------

var meetingPathDateRe = regexp.MustCompile(`^Meetings/[^/]+/(\d{4})/(\d{2})/(\d{2})/`)

type dateMatchesPath struct{}

func (dateMatchesPath) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	m := meetingPathDateRe.FindStringSubmatch(n.RelPath)
	if m == nil || !hasParsedFM(n) {
		return nil
	}
	date, ok := n.FM.Fields["date"].(string)
	if !ok {
		return nil // missing/mistyped date is a schema finding
	}
	pathDate := fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3])
	if date == pathDate {
		return nil
	}
	return []lint.Finding{finding(lint.Error, n.RelPath, n.FM.Lines["date"], false,
		"frontmatter date %s does not match path date %s", date, pathDate)}
}

// ---- meeting-time-matches-filename ---------------------------------------------

var hhmmPrefixRe = regexp.MustCompile(`^([01][0-9]|2[0-3])([0-5][0-9]) - `)

type timeMatchesFilename struct{}

func (timeMatchesFilename) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "meeting" || !hasParsedFM(n) {
		return nil
	}
	m := hhmmPrefixRe.FindStringSubmatch(baseOf(n.RelPath))
	timeVal, ok := n.FM.Fields["time"].(string)
	if m == nil || !ok {
		return nil
	}
	start := strings.SplitN(timeVal, " - ", 2)[0]
	if strings.ReplaceAll(start, ":", "") == m[1]+m[2] {
		return nil
	}
	return []lint.Finding{finding(lint.Warning, n.RelPath, n.FM.Lines["time"], false,
		"filename time %s%s does not match frontmatter start time %q", m[1], m[2], start)}
}

// ---- meeting-tags-domain ----------------------------------------------------------

type tagsDomain struct{}

func (tagsDomain) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	typ := typedNote(ctx, n)
	if (typ != "meeting" && typ != "daily-brief") || !hasParsedFM(n) {
		return nil
	}
	parts := strings.SplitN(n.RelPath, "/", 3)
	if len(parts) < 3 || parts[0] != "Meetings" {
		return nil
	}
	domain := strings.ToLower(parts[1])
	tags, _, ok := n.FM.StringList("tags")
	if !ok {
		return nil // missing tags is a schema finding
	}
	if contains(tags, domain) {
		return nil
	}
	return []lint.Finding{finding(lint.Error, n.RelPath, n.FM.Lines["tags"], true,
		"tags must contain the path domain %q", domain)}
}

var domainMsgRe = regexp.MustCompile(`^tags must contain the path domain "(.+)"$`)

func (tagsDomain) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := domainMsgRe.FindStringSubmatch(f.Message)
	if m == nil || !hasParsedFM(n) {
		return nil, nil
	}
	domain := m[1]
	line, ok := getLine(n.Src, n.FM.Lines["tags"])
	if !ok {
		return nil, nil
	}
	// Inline flow list: append. Block list: insert a new item line.
	if flowRe := regexp.MustCompile(`^(\s*tags:\s*\[)(.*)(\]\s*)$`); flowRe.Match(line) {
		mm := flowRe.FindSubmatch(line)
		items := string(mm[2])
		if strings.TrimSpace(items) == "" {
			items = domain
		} else {
			items += ", " + domain
		}
		newSrc, ok := replaceLine(n.Src, n.FM.Lines["tags"], []byte(string(mm[1])+items+string(mm[3])))
		if !ok {
			return nil, nil
		}
		return &lint.FixResult{NewSrc: newSrc}, nil
	}
	if regexp.MustCompile(`^\s*tags:\s*$`).Match(line) {
		newSrc, ok := insertLineAfter(n.Src, n.FM.Lines["tags"], []byte("  - "+domain))
		if !ok {
			return nil, nil
		}
		return &lint.FixResult{NewSrc: newSrc}, nil
	}
	return nil, nil
}

// ---- project-category-matches-path ---------------------------------------------

type categoryMatchesPath struct{}

func (categoryMatchesPath) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "project" || !hasParsedFM(n) {
		return nil
	}
	cat, ok := n.FM.Fields["category"].(string)
	if !ok {
		return nil
	}
	parts := strings.SplitN(n.RelPath, "/", 3)
	if len(parts) < 3 {
		return nil
	}
	if cat == parts[1] {
		return nil
	}
	// Path is the documented source of truth (docs/LINT-RULES.md).
	return []lint.Finding{finding(lint.Error, n.RelPath, n.FM.Lines["category"], true,
		"category %q does not match folder %q", cat, parts[1])}
}

func (categoryMatchesPath) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if !hasParsedFM(n) {
		return nil, nil
	}
	parts := strings.SplitN(n.RelPath, "/", 3)
	if len(parts) < 3 {
		return nil, nil
	}
	lineNo := n.FM.Lines["category"]
	line, ok := getLine(n.Src, lineNo)
	if !ok {
		return nil, nil
	}
	keyRe := regexp.MustCompile(`^(\s*category:\s*).*$`)
	if !keyRe.Match(line) {
		return nil, nil
	}
	newLine := keyRe.ReplaceAll(line, []byte("${1}"+parts[1]))
	newSrc, ok := replaceLine(n.Src, lineNo, newLine)
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- project-status-vocab ---------------------------------------------------------

type statusVocab struct {
	allow []string
}

func (r statusVocab) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if typedNote(ctx, n) != "project" || !hasParsedFM(n) {
		return nil
	}
	status, ok := n.FM.Fields["status"].(string)
	if !ok || contains(r.allow, status) {
		return nil
	}
	return []lint.Finding{finding(lint.Warning, n.RelPath, n.FM.Lines["status"], false,
		"status %q is not in the allowlist %v", status, r.allow)}
}
