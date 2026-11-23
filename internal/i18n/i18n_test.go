package i18n

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		pref   string
		system []string
		want   string
	}{
		{"", []string{"es-MX", "en-US"}, "es"},
		{"", []string{"fr-FR", "es-ES"}, "es"},
		{"", []string{"fr-FR"}, Fallback},
		{"", nil, Fallback},
		{"", []string{"es_MX.UTF-8"}, "es"},
		{"en", []string{"es-MX"}, "en"},
		{"xx", []string{"es-MX"}, "es"},
	} {
		if got := Resolve(tc.pref, tc.system); got != tc.want {
			t.Errorf("Resolve(%q, %q) = %q; quiero %q", tc.pref, tc.system, got, tc.want)
		}
	}
}

func TestTranslate(t *testing.T) {
	cats, err := load(fstest.MapFS{
		"locales/en.json": {Data: []byte(`{"_name":"English","hi":"Hi {name}","n":{"one":"{n} file","other":"{n} files"},"only":"only en"}`)},
		"locales/es.json": {Data: []byte(`{"_name":"Español","hi":"Hola {name}","n":{"one":"un archivo","other":"{n} archivos"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := catalogs
	catalogs = cats
	t.Cleanup(func() { catalogs = saved; current.Store(Fallback) })

	SetCurrent("es")
	for _, tc := range []struct{ got, want string }{
		{T("hi", "name", "Ana"), "Hola Ana"},
		{T("n", "n", 1), "un archivo"},
		{T("n", "n", 3), "3 archivos"},
		{T("only"), "only en"},
		{T("missing"), "missing"},
		{TL("en", "n", "n", 1), "1 file"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q; quiero %q", tc.got, tc.want)
		}
	}
	if got := Languages(); len(got) != 2 || got[0] != (Language{"en", "English"}) || got[1].Name != "Español" {
		t.Errorf("Languages = %v", got)
	}

	inner := errors.New("disco lleno")
	err = E("hi", "name", "Ana", "err", inner)
	if err.Error() != "Hola Ana" || !errors.Is(err, inner) {
		t.Errorf("E: %q, Is=%v", err, errors.Is(err, inner))
	}
	SetCurrent("en")
	if err.Error() != "Hi Ana" {
		t.Errorf("E después de cambiar de idioma: %q", err)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, data := range map[string]string{
		"sin _name":        `{"a":"b"}`,
		"plural sin other": `{"_name":"X","a":{"one":"b"}}`,
		"valor numérico":   `{"_name":"X","a":3}`,
	} {
		_, err := load(fstest.MapFS{"locales/en.json": {Data: []byte(data)}})
		if err == nil {
			t.Errorf("%s: se aceptó", name)
		}
	}
}

var placeholder = regexp.MustCompile(`\{[a-zA-Z0-9_]+\}`)

// placeholders son los {datos} de todas las formas de un texto.
func placeholders(e entry) []string {
	set := map[string]bool{}
	for _, s := range append(slices.Collect(maps.Values(e.plural)), e.text) {
		for _, p := range placeholder.FindAllString(s, -1) {
			set[p] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// TestCatalogsComplete: todos los idiomas tienen las mismas claves y los
// mismos {datos} que Fallback.
func TestCatalogsComplete(t *testing.T) {
	ref := catalogs[Fallback].entries
	for code, c := range catalogs {
		for k, e := range ref {
			got, ok := c.entries[k]
			if !ok {
				t.Errorf("%s.json: falta %q", code, k)
				continue
			}
			if (got.plural == nil) != (e.plural == nil) {
				t.Errorf("%s.json: %q debe ser %s, como en %s.json", code, k, map[bool]string{true: "texto", false: "plural"}[e.plural == nil], Fallback)
			}
			if a, b := placeholders(got), placeholders(e); !slices.Equal(a, b) {
				t.Errorf("%s.json: %q usa %v y %s.json %v", code, k, a, Fallback, b)
			}
		}
		for k := range c.entries {
			if _, ok := ref[k]; !ok {
				t.Errorf("%s.json: %q no está en %s.json", code, k, Fallback)
			}
		}
	}
}

// keyUses encuentran las claves que usa el código: i18n.T("…"), i18n.E("…"),
// i18n.TL(lang, "…") en Go; t("…") en la página y data-i18n*="…" en el HTML.
var keyUses = []*regexp.Regexp{
	regexp.MustCompile(`i18n\.(?:T|E)\(\s*"([^"]+)"`),
	regexp.MustCompile(`i18n\.TL\([^,()]+,\s*"([^"]+)"`),
	regexp.MustCompile(`(?:^|[^\w.$])t\(\s*"([^"]+)"`),
	regexp.MustCompile(`data-i18n(?:-[a-z-]+)?="([^"]+)"`),
}

// sources lee el código de LanChat que puede usar traducciones.
func sources(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "android", "dist", "docs", "locales", "magefiles", "packaging":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".js", ".html":
			if strings.HasSuffix(p, "_test.go") && !strings.HasSuffix(p, "demo_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[p] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestKeysUsed: toda clave que usa el código existe, y toda clave del
// catálogo aparece en el código (entre comillas, también en tablas de claves).
func TestKeysUsed(t *testing.T) {
	ref := catalogs[Fallback].entries
	src := sources(t)
	var all strings.Builder
	for p, s := range src {
		all.WriteString(s)
		for _, re := range keyUses {
			for _, m := range re.FindAllStringSubmatch(s, -1) {
				if _, ok := ref[m[1]]; !ok {
					t.Errorf("%s usa la clave %q, que no está en %s.json", p, m[1], Fallback)
				}
			}
		}
	}
	text := all.String()
	for k := range ref {
		if !strings.Contains(text, `"`+k+`"`) {
			t.Errorf("%s.json: la clave %q no se usa", Fallback, k)
		}
	}
}

// TestAndroidLanguages: la app de Android ofrece los mismos idiomas
// (locales_config.xml) y trae sus textos traducidos (values-<idioma>).
func TestAndroidLanguages(t *testing.T) {
	res := filepath.Join("..", "..", "android", "app", "src", "main", "res")
	b, err := os.ReadFile(filepath.Join(res, "xml", "locales_config.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, m := range regexp.MustCompile(`<locale android:name="([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
		offered = append(offered, normalize(m[1]))
	}
	var want []string
	for _, l := range Languages() {
		want = append(want, l.Code)
		if l.Code == Fallback {
			continue // values/ (sin sufijo) está en inglés
		}
		dir := "values-" + l.Code
		if lang, region, ok := strings.Cut(l.Code, "-"); ok {
			dir = "values-" + lang + "-r" + strings.ToUpper(region)
		}
		if _, err := os.Stat(filepath.Join(res, dir, "strings.xml")); err != nil {
			t.Errorf("falta android/app/src/main/res/%s/strings.xml para %q", dir, l.Code)
		}
	}
	slices.Sort(offered)
	if !slices.Equal(offered, want) {
		t.Errorf("locales_config.xml ofrece %v; los idiomas son %v", offered, want)
	}
}
