package update

import (
	"cmp"
	"regexp"
	"strconv"
)

// Version es una versión de LanChat como la escribe git describe (sin la v):
// "0.19.0", "0.20.0-rc.1", "0.19.0-6-g1286232" (6 commits después de la
// etiqueta) o "0.19.0-dirty" (con cambios sin commit).
type Version struct {
	Major, Minor, Patch int
	// Pre es la parte preliminar ("rc.1"); vacía en una versión final.
	Pre string
	// Commits son los commits después de la etiqueta (compilación local).
	Commits int
	Dirty   bool
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([A-Za-z][0-9A-Za-z.]*))?(?:-(\d+)-g[0-9a-f]+)?(-dirty)?$`)

// Parse interpreta una versión; ok es false si no tiene la forma X.Y.Z
// ("dev", un hash suelto).
func Parse(s string) (v Version, ok bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Pre = m[4]
	v.Commits, _ = strconv.Atoi(m[5]) // vacío = 0
	v.Dirty = m[6] != ""
	if v.Pre == "dirty" && !v.Dirty && v.Commits == 0 { // "0.19.0-dirty"
		v.Pre, v.Dirty = "", true
	}
	return v, true
}

// Release indica si es una versión publicada: sin parte preliminar, sin
// commits después de la etiqueta y sin cambios sin commit.
func (v Version) Release() bool { return v.Pre == "" && v.Commits == 0 && !v.Dirty }

// Compare devuelve -1, 0 o 1. Una versión preliminar va antes que la final
// (0.20.0-rc.1 < 0.20.0) y los commits después de una etiqueta, después de
// ella (0.19.0 < 0.19.0-6-g…).
func (v Version) Compare(w Version) int {
	if c := cmp.Compare(v.Major, w.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, w.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, w.Patch); c != 0 {
		return c
	}
	switch {
	case v.Pre == "" && w.Pre != "":
		return 1
	case v.Pre != "" && w.Pre == "":
		return -1
	}
	if c := cmp.Compare(v.Pre, w.Pre); c != 0 {
		return c
	}
	return cmp.Compare(v.Commits, w.Commits)
}

// Newer indica si la versión a es posterior a b. Si alguna no se entiende,
// no lo es.
func Newer(a, b string) bool {
	va, ok1 := Parse(a)
	vb, ok2 := Parse(b)
	return ok1 && ok2 && va.Compare(vb) > 0
}

// Updatable indica si una compilación con esa versión puede actualizarse
// sola: no las de desarrollo ("dev", con cambios sin commit), que se
// reemplazarían por la publicada sin querer.
func Updatable(current string) bool {
	v, ok := Parse(current)
	return ok && !v.Dirty
}
