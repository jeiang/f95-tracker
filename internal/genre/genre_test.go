package genre

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jeiang/f95-tracker/internal/testutil"
)

// seedSynonyms is SYN from docs/research/f95-genre-text/parse_genre.py (the two
// explicit None entries, deflowering/defloration, are simply absent).
var seedSynonyms = func() map[string]Target {
	m := map[string]Target{}
	for k, v := range map[string]string{
		"futa": "futa-trans", "futanari": "futa-trans", "futatrans": "futa-trans",
		"3dgc": "3dcg", "haren": "harem", "oral": "oral-sex", "blowjob": "oral-sex",
		"anal": "anal-sex", "vaginal": "vaginal-sex", "femdom": "femaledomination",
		"netori": "ntr", "netorare": "ntr", "scifi": "sci-fi", "toys": "sex-toys",
		"largebreasts": "big-tits", "boobjob": "titfuck", "boobsjob": "titfuck",
		"titjob": "titfuck", "superpower": "superpowers", "possesion": "possession",
		"violence": "graphic-violence", "group": "group-sex", "yuri": "lesbian",
		"datingsim": "dating-sim", "pointandclick": "point-click",
		"femaleprotagonist": "female-protagonist", "futaprotagonist": "futa-trans-protagonist",
	} {
		m[k] = Target{Name: v}
	}
	return m
}()

func seedVocab(t testing.TB) map[string]string {
	t.Helper()
	f, err := os.Open("testdata/f95-tag-vocabulary.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, row := range rows[1:] { // header: slug, label, f95checker_id
		if s := row[0]; !strings.HasPrefix(s, "asset-") && s != "unknown" {
			slugs = append(slugs, s)
		}
	}
	return Vocabulary(slugs)
}

type sample struct {
	ThreadID  string   `json:"thread_id"`
	F95Tags   []string `json:"f95_tags"`
	GenreText string   `json:"genre_text"`
}

func loadSamples(t testing.TB) []sample {
	t.Helper()
	f, err := os.Open("testdata/samples.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []sample
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var s sample
			if e := json.Unmarshal(line, &s); e != nil {
				t.Fatal(e)
			}
			out = append(out, s)
		}
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSamplesGolden(t *testing.T) {
	vocab := seedVocab(t)
	samples := loadSamples(t)
	if len(samples) != 42 {
		t.Fatalf("samples = %d, want 42", len(samples))
	}
	for _, s := range samples {
		t.Run(s.ThreadID, func(t *testing.T) {
			items := Parse(s.GenreText)
			matches := Matches(items, vocab, seedSynonyms)
			testutil.Golden(t, "samples/"+s.ThreadID, map[string]any{
				"items":    items,
				"matches":  matches,
				"f95_only": F95Only(matches, s.F95Tags),
			})
		})
	}
}

// parse_genre.py gives 785 phrases: 719 exact, 30 synonym/variant, 36 none (the research prose says 31/35; its exact count and total agree).
func TestSamplesResearchTotals(t *testing.T) {
	vocab := seedVocab(t)
	counts := map[Kind]int{}
	total := 0
	for _, s := range loadSamples(t) {
		for _, it := range Parse(s.GenreText) {
			ms := classify(it, vocab, seedSynonyms)
			counts[ms[0].Kind]++
			total++
		}
	}
	// Prose items are counted by the research as no F95 tag.
	got := [4]int{total, counts[Exact], counts[Synonym], counts[NoMatch] + counts[Prose]}
	if want := [4]int{785, 719, 30, 36}; got != want {
		t.Errorf("total/exact/synonym/none = %v, want %v", got, want)
	}
}

func TestParse(t *testing.T) {
	type row struct {
		raw  string
		q    Qualifier
		poss bool
	}
	tests := []struct {
		name, text string
		want       []row
	}{
		{"commas and trim", " A , b.\n", []row{{"A", Present, false}, {"b", Present, false}}},
		{"heading sets section, rest is first item", "Planned: Foo, Bar\nBaz", []row{{"Foo", Planned, false}, {"Bar", Planned, false}, {"Baz", Planned, false}}},
		{"future and currently", "Future: A\nCurrently: B", []row{{"A", Planned, false}, {"B", Present, false}}},
		{"blank lines and spoilers keep section", "Planned:\n\n[[spoiler=x]]\nA\n[[/spoiler]]\n\nB", []row{{"A", Planned, false}, {"B", Planned, false}}},
		{"possible is planned and flagged", "Possible (Most to least possible): A\nB", []row{{"A", Planned, true}, {"B", Planned, true}}},
		{"heading switches possible off", "Possible: A\nCurrently: B", []row{{"A", Planned, true}, {"B", Present, false}}},
		{"bullets", "- A\n* B\n\u2022 C", []row{{"A", Present, false}, {"B", Present, false}, {"C", Present, false}}},
		{"for now dropped", "Foo for now, Bar", []row{{"Foo", Present, false}, {"Bar", Present, false}}},
		{"ellipsis splits", "A...B", []row{{"A", Present, false}, {"B", Present, false}}},
		{"sentence period splits, dotted word does not", "A. B, v1.5 thing", []row{{"A", Present, false}, {"B", Present, false}, {"v1.5 thing", Present, false}}},
		{"comma in parentheses kept", "Foo (a, b), Bar", []row{{"Foo (a, b)", Present, false}, {"Bar", Present, false}}},
		{"optional paren", "Harem (Optional), Pregnancy(optional)", []row{{"Harem (Optional)", Optional, false}, {"Pregnancy(optional)", Optional, false}}},
		{"optional prefix", "Optional trap", []row{{"Optional trap", Optional, false}}},
		{"optional keeps possible flag", "Possible: Harem (avoidable)", []row{{"Harem (avoidable)", Optional, true}}},
		{"other parenthetical is a note not optional", "Vore (soft)", []row{{"Vore (soft)", Present, false}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []row
			for _, it := range Parse(tc.text) {
				got = append(got, row{it.Raw, it.Qualifier, it.Possible})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseItemFields(t *testing.T) {
	it := Parse("Vore (soft)")[0]
	if it.Phrase != "Vore" || !reflect.DeepEqual(it.Notes, []string{"soft"}) || it.Qualifier != Present {
		t.Errorf("got %+v", it)
	}
	it = Parse("Harem (Optional)")[0]
	if it.Phrase != "Harem" || len(it.Notes) != 0 {
		t.Errorf("got %+v", it)
	}
}

func TestParseProse(t *testing.T) {
	prose := Parse("This game is about many things (really)")
	if len(prose) != 1 || !prose[0].Prose {
		t.Fatalf("got %+v", prose)
	}
	// Prose keeps the section qualifier even with an optional marker.
	p := Parse("Planned: this game is optional and has etc (a)")[0]
	if !p.Prose || p.Qualifier != Planned {
		t.Errorf("got %+v", p)
	}
	// Short phrase with a note is not prose.
	if Parse("Vore is (soft)")[0].Prose {
		t.Error("short phrase flagged as prose")
	}
}

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"Male protagonist": "maleprotagonist", "Futa/trans": "futatrans", "Hand-job ": "handjob", "3DCG": "3dcg", "É": "",
	} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatches(t *testing.T) {
	vocab := Vocabulary([]string{"bdsm", "anal-sex", "vaginal-sex", "harem", "male-protagonist", "oral-sex"})
	syn := map[string]Target{"blowjob": {Name: "oral-sex"}, "anal": {Name: "anal-sex"}, "vaginal": {Name: "vaginal-sex"}}
	tests := []struct {
		name, text string
		want       []Match
	}{
		{"exact", "Male protagonist", []Match{{Raw: "Male protagonist", Kind: Exact, Target: Target{Name: "male-protagonist"}, Qualifier: Present}}},
		{"synonym", "Blowjob", []Match{{Raw: "Blowjob", Kind: Synonym, Target: Target{Name: "oral-sex"}, Qualifier: Present}}},
		{"modifier", "Mild BDSM", []Match{{Raw: "Mild BDSM", Kind: Synonym, Target: Target{Name: "bdsm"}, Qualifier: Present, Modifier: "mild"}}},
		{"quoted modifier", "'Soft' BDSM", []Match{{Raw: "'Soft' BDSM", Kind: Synonym, Target: Target{Name: "bdsm"}, Qualifier: Present, Modifier: "soft"}}},
		{"compound", "Anal/Vaginal", []Match{
			{Raw: "Anal/Vaginal", Kind: Synonym, Target: Target{Name: "anal-sex"}, Qualifier: Present},
			{Raw: "Anal/Vaginal", Kind: Synonym, Target: Target{Name: "vaginal-sex"}, Qualifier: Present},
		}},
		{"compound with unknown side is no match", "Anal/Elves", []Match{{Raw: "Anal/Elves", Kind: NoMatch, Target: Target{Name: "Anal/Elves", Custom: true}, Qualifier: Present}}},
		{"no match is custom", "Elves", []Match{{Raw: "Elves", Kind: NoMatch, Target: Target{Name: "Elves", Custom: true}, Qualifier: Present}}},
		{"note kept", "Harem (big)", []Match{{Raw: "Harem (big)", Kind: Exact, Target: Target{Name: "harem"}, Qualifier: Present, Note: "big"}}},
		{"present beats planned", "Planned: Harem\nCurrently: harem", []Match{{Raw: "harem", Kind: Exact, Target: Target{Name: "harem"}, Qualifier: Present}}},
		{"present first wins over later planned", "Harem\nPlanned: HAREM", []Match{{Raw: "Harem", Kind: Exact, Target: Target{Name: "harem"}, Qualifier: Present}}},
		{"synonym and exact collapse", "Planned: Oral sex\nCurrently: Blowjob", []Match{{Raw: "Blowjob", Kind: Synonym, Target: Target{Name: "oral-sex"}, Qualifier: Present}}},
		{"custom variants collapse", "Elves, elves", []Match{{Raw: "Elves", Kind: NoMatch, Target: Target{Name: "Elves", Custom: true}, Qualifier: Present}}},
		{"optional beats planned", "Planned: Harem\nHarem (optional)", []Match{{Raw: "Harem (optional)", Kind: Exact, Target: Target{Name: "harem"}, Qualifier: Optional}}},
		{"possible flagged", "Possible: Harem", []Match{{Raw: "Harem", Kind: Exact, Target: Target{Name: "harem"}, Qualifier: Planned, Possible: true}}},
		{"prose kept apart", "This game is about many things (really), This game is about many things (really)", []Match{
			{Raw: "This game is about many things (really)", Kind: Prose, Qualifier: Present, Note: "really"},
			{Raw: "This game is about many things (really)", Kind: Prose, Qualifier: Present, Note: "really"},
		}},
		{"user synonym to custom tag", "Deflowering", []Match{{Raw: "Deflowering", Kind: Synonym, Target: Target{Name: "Virgin", Custom: true}, Qualifier: Present}}},
	}
	syn["deflowering"] = Target{Name: "Virgin", Custom: true}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(Parse(tc.text), vocab, syn); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestNeedsLookAndF95Only(t *testing.T) {
	vocab := Vocabulary([]string{"harem", "bdsm", "ntr"})
	ms := Matches(Parse("Harem, Elves, Mild BDSM\nPossible: NTR"), vocab, nil)
	var look []string
	for _, m := range ms {
		if m.NeedsLook() {
			look = append(look, m.Raw)
		}
	}
	if want := []string{"Elves", "Mild BDSM", "NTR"}; !reflect.DeepEqual(look, want) {
		t.Errorf("needs look %v, want %v", look, want)
	}
	got := F95Only(ms, []string{"harem", "teasing", "ntr", "bdsm", "groping"})
	if want := []string{"teasing", "groping"}; !reflect.DeepEqual(got, want) {
		t.Errorf("f95_only %v, want %v", got, want)
	}
}
