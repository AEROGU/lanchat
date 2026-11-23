// Package i18n traduce los textos de LanChat: los de la página (que recibe
// el catálogo completo, ver Messages) y los que arma Go (errores, bandeja,
// diálogos de Windows, notificaciones).
//
// Cada idioma es un archivo locales/<código>.json con claves planas:
//
//	{
//	  "_name": "English",
//	  "select.delete": "Delete for everyone",
//	  "select.count": { "one": "{n} selected", "other": "{n} selected" }
//	}
//
// "_name" es el nombre del idioma escrito en ese idioma (lo muestra Ajustes).
// Los {datos} se sustituyen con los argumentos; un valor con categorías de
// plural (las de CLDR: zero, one, two, few, many, other) elige la forma según
// el argumento n. Para agregar un idioma basta copiar en.json con otro código
// y traducirlo (más res/values-<código>/strings.xml en Android): aparece solo
// en Ajustes, y las pruebas fallan si le falta una clave o un {dato}.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync/atomic"
)

// Fallback es el idioma cuando el del sistema no está traducido, y de donde
// salen las claves que le falten a otro idioma.
const Fallback = "en"

//go:embed locales/*.json
var files embed.FS

// entry es un texto: uno solo o una forma por categoría de plural.
type entry struct {
	text   string
	plural map[string]string
}

func (e *entry) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.text); err == nil {
		return nil
	}
	if err := json.Unmarshal(b, &e.plural); err != nil {
		return fmt.Errorf("se esperaba un texto o un objeto con formas de plural: %w", err)
	}
	if e.plural["other"] == "" {
		return fmt.Errorf("al plural le falta la forma \"other\"")
	}
	return nil
}

func (e entry) MarshalJSON() ([]byte, error) {
	if e.plural != nil {
		return json.Marshal(e.plural)
	}
	return json.Marshal(e.text)
}

type catalog struct {
	name    string
	entries map[string]entry
}

var (
	catalogs = mustLoad(files)
	current  atomic.Value // string
)

func mustLoad(fsys fs.FS) map[string]*catalog {
	c, err := load(fsys)
	if err != nil {
		panic("i18n: " + err.Error())
	}
	return c
}

func load(fsys fs.FS) (map[string]*catalog, error) {
	names, err := fs.Glob(fsys, "locales/*.json")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*catalog, len(names))
	for _, name := range names {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		var raw map[string]entry
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		code := strings.TrimSuffix(path.Base(name), ".json")
		c := &catalog{name: raw["_name"].text, entries: raw}
		if c.name == "" {
			return nil, fmt.Errorf("%s: falta \"_name\"", name)
		}
		delete(raw, "_name")
		out[normalize(code)] = c
	}
	if out[Fallback] == nil {
		return nil, fmt.Errorf("falta locales/%s.json", Fallback)
	}
	return out, nil
}

// Language es un idioma disponible.
type Language struct {
	// Code es el código del archivo ("en", "es", "pt-br").
	Code string `json:"code"`
	// Name es el nombre del idioma en ese idioma ("English", "Español").
	Name string `json:"name"`
}

// Languages devuelve los idiomas disponibles, ordenados por código.
func Languages() []Language {
	out := make([]Language, 0, len(catalogs))
	for code, c := range catalogs {
		out = append(out, Language{Code: code, Name: c.name})
	}
	slices.SortFunc(out, func(a, b Language) int { return strings.Compare(a.Code, b.Code) })
	return out
}

// Supported indica si hay traducción para code.
func Supported(code string) bool { return catalogs[normalize(code)] != nil }

// normalize deja una etiqueta de idioma como los nombres de archivo:
// "es_MX.UTF-8" → "es-mx".
func normalize(tag string) string {
	tag, _, _ = strings.Cut(tag, ".")
	tag, _, _ = strings.Cut(tag, "@")
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
}

// Match devuelve el primer idioma disponible de tags (en orden de
// preferencia, como los da el sistema): la etiqueta completa ("pt-BR") o su
// idioma base ("pt"). Sin ninguno, Fallback.
func Match(tags ...string) string {
	for _, t := range tags {
		t = normalize(t)
		if catalogs[t] != nil {
			return t
		}
		if base, _, ok := strings.Cut(t, "-"); ok && catalogs[base] != nil {
			return base
		}
	}
	return Fallback
}

// Resolve elige el idioma: pref si el usuario eligió uno disponible ("" =
// automático); si no, el primero de system que esté traducido.
func Resolve(pref string, system []string) string {
	if pref != "" && Supported(pref) {
		return normalize(pref)
	}
	return Match(system...)
}

// SetCurrent cambia el idioma de T y de los errores (Error).
func SetCurrent(code string) {
	if !Supported(code) {
		code = Fallback
	}
	current.Store(normalize(code))
}

// Current es el idioma en uso (Fallback hasta que se llame SetCurrent).
func Current() string {
	if c, ok := current.Load().(string); ok {
		return c
	}
	return Fallback
}

// T traduce key al idioma en uso; args son pares nombre, valor para los
// {datos} del texto (n además elige la forma de plural).
func T(key string, args ...any) string { return TL(Current(), key, args...) }

// TL es T en el idioma lang.
func TL(lang, key string, args ...any) string {
	e, ok := lookup(lang, key)
	if !ok {
		return key
	}
	text := e.text
	if e.plural != nil {
		text = e.plural[pluralCategory(lang, args)]
		if text == "" {
			text = e.plural["other"]
		}
	}
	return format(text, args)
}

func lookup(lang, key string) (entry, bool) {
	if c := catalogs[normalize(lang)]; c != nil {
		if e, ok := c.entries[key]; ok {
			return e, true
		}
	}
	e, ok := catalogs[Fallback].entries[key]
	return e, ok
}

// pluralCategory elige la forma según el argumento n. Go no trae las reglas
// de plural de CLDR; "one" para 1 y "other" para lo demás sirve para los
// idiomas de ahora. La página usa Intl.PluralRules, que las tiene todas.
func pluralCategory(_ string, args []any) string {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "n" {
			if fmt.Sprint(args[i+1]) == "1" {
				return "one"
			}
			break
		}
	}
	return "other"
}

func format(text string, args []any) string {
	if len(args) < 2 || !strings.Contains(text, "{") {
		return text
	}
	pairs := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		pairs = append(pairs, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// Messages devuelve el catálogo de lang para la página, con las claves que
// le falten tomadas de Fallback.
func Messages(lang string) map[string]any {
	out := make(map[string]any, len(catalogs[Fallback].entries))
	for k, e := range catalogs[Fallback].entries {
		out[k] = e
	}
	if c := catalogs[normalize(lang)]; c != nil {
		for k, e := range c.entries {
			out[k] = e
		}
	}
	return out
}

// Error es un error con texto traducible: se traduce al idioma en uso cada
// vez que se lee, así sirve también para errores declarados en variables.
type Error struct {
	Key  string
	Args []any
}

// E crea un Error; args como en T. Un argumento que sea error queda
// disponible para errors.Is y errors.As.
func E(key string, args ...any) error { return &Error{Key: key, Args: args} }

func (e *Error) Error() string { return T(e.Key, e.Args...) }

func (e *Error) Unwrap() error {
	for i := 1; i < len(e.Args); i += 2 {
		if err, ok := e.Args[i].(error); ok {
			return err
		}
	}
	return nil
}

// EnvOverride es la variable de entorno que, si tiene un idioma, reemplaza
// al del sistema (pruebas, capturas de pantalla).
const EnvOverride = "LANCHAT_LANG"

// System devuelve los idiomas del sistema en orden de preferencia.
func System() []string {
	if l := os.Getenv(EnvOverride); l != "" {
		return []string{l}
	}
	return systemLanguages()
}
