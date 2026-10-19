package incident

import (
	"fmt"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jssort"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
)

// Summary is one fingerprint group of Summarize: the fp, component, kind and summary of its
// first entry, how many entries it has, the `at` of the first and last, and the distinct
// truthy projects.
type Summary struct {
	fp, component, kind, summary value
	count                        int
	first, last                  value
	projects                     *jsjson.OrderedSet
}

// value is a property value as the JS holds it: defined or not, possibly an inherited method.
type value struct {
	v       any
	present bool
}

func propertyValue(e any, key string) value {
	v, present := property(e, key)
	return value{v, present}
}

// Summarize groups entries by fingerprint (compared as JS Map keys) in first-seen order and
// sorts the groups most frequent first, then most recent first, with V8's sort (package
// jssort), so even ties come out in the JS order.
func Summarize(entries []any) []Summary {
	var groups []*Summary
	byFP := map[any]*Summary{}
	for _, e := range entries {
		key := jsjson.SameValueZeroKey(property(e, "fp"))
		s := byFP[key]
		if s == nil {
			at := propertyValue(e, "at")
			s = &Summary{
				fp:        propertyValue(e, "fp"),
				component: propertyValue(e, "component"),
				kind:      propertyValue(e, "kind"),
				summary:   propertyValue(e, "summary"),
				first:     at,
				projects:  &jsjson.OrderedSet{},
			}
			byFP[key] = s
			groups = append(groups, s)
		}
		s.count++
		s.last = propertyValue(e, "at")
		if project, present := property(e, "project"); jsjson.Truthy(project) {
			s.projects.Add(project, present)
		}
	}
	summaries := make([]Summary, len(groups))
	for i, s := range groups {
		summaries[i] = *s
	}
	jssort.Sort(summaries, compareSummaries)
	return summaries
}

// compareSummaries is the JS comparator `b.count - a.count || (a.last < b.last ? 1 : -1)`. It
// never returns 0, so it is inconsistent for two groups with equal counts whose `last` values
// are equal or incomparable (a number and a non-numeric string, say): their order then depends
// on every comparison the sort makes.
func compareSummaries(a, b Summary) int {
	if a.count != b.count {
		return b.count - a.count
	}
	if jsjson.GreaterThan(relationalOperand(b.last.v, b.last.present), relationalOperand(a.last.v, a.last.present)) {
		return 1
	}
	return -1
}

// Object is the summary as JSON.stringify writes `{...s, projects: [...s.projects]}`: keys in
// that order, undefined values and methods omitted.
func (s Summary) Object() *jsjson.Object {
	o := jsjson.NewObject()
	for _, field := range []struct {
		key string
		value
	}{
		{"fp", s.fp}, {"component", s.component}, {"kind", s.kind}, {"summary", s.summary},
		{"count", value{float64(s.count), true}}, {"first", s.first}, {"last", s.last},
		{"projects", value{s.projects.Values(), true}},
	} {
		if _, isMethod := field.v.(nativeMethod); field.present && !isMethod {
			o.Set(field.key, field.v)
		}
	}
	return o
}

// ReportLine is the report text for s:
// `  3x <fp> <component>/<kind> [<projects>] <summary>  (first <at16>, last <at16>)`.
func (s Summary) ReportLine() (string, error) {
	first, err := sliceText(s.first, "first", 16)
	if err != nil {
		return "", err
	}
	last, err := sliceText(s.last, "last", 16)
	if err != nil {
		return "", err
	}
	projects := jsjson.Join(s.projects.Values(), ",")
	if projects == "" {
		projects = "-"
	}
	return fmt.Sprintf("%3dx %s %s/%s [%s] %s  (first %s, last %s)", s.count,
		templateText(s.fp.v, s.fp.present), templateText(s.component.v, s.component.present),
		templateText(s.kind.v, s.kind.present), projects, templateText(s.summary.v, s.summary.present), first, last), nil
}

// sliceText is `${r[name].slice(0, end)}`: strings slice by UTF-16 code units, arrays by
// elements (then join with ","); other values have no slice and throw.
func sliceText(v value, name string, end int) (string, error) {
	switch s := v.v.(type) {
	case string:
		return jsstr.Slice(s, 0, end), nil
	case []any:
		return jsjson.TemplateString(s[:min(len(s), end)]), nil
	case nil:
		return "", &ShapeError{"Cannot read properties of " + jsjson.TemplateStringOf(nil, v.present) + " (reading 'slice')"}
	}
	return "", &ShapeError{"r." + name + ".slice is not a function"}
}

// ListLine is the list text for an entry: `<at> <fp> <component>/<kind> <project or ->: <summary>`.
func ListLine(e any) string {
	project := "-"
	if v, present := property(e, "project"); jsjson.Truthy(v) {
		project = templateText(v, present)
	}
	return templateText(property(e, "at")) + " " + templateText(property(e, "fp")) + " " +
		templateText(property(e, "component")) + "/" + templateText(property(e, "kind")) + " " +
		project + ": " + templateText(property(e, "summary"))
}
