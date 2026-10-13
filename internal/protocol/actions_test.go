package protocol

import "testing"

func TestValidateReaction(t *testing.T) {
	// Sin escribirlos en el archivo: son caracteres invisibles.
	zwj, vs16 := string(rune(0x200D)), string(rune(0xFE0F))
	family := "👨" + zwj + "👩" + zwj + "👧" // tres emoji unidos en uno
	valid := []string{"👍", "❤" + vs16, "😂", "🙏", "👍🏽", family, "🇲🇽", "✅", "⭐"}
	for _, s := range valid {
		if err := ValidateReaction(s); err != nil {
			t.Errorf("%q debía ser válida: %v", s, err)
		}
	}
	invalid := []string{"", "hola", "👍 ", "a", "1", "<b>", zwj, vs16, "👍" + string(rune(10)),
		"😀😀😀😀😀😀😀😀😀😀😀"}
	for _, s := range invalid {
		if err := ValidateReaction(s); err == nil {
			t.Errorf("%q no debía ser válida", s)
		}
	}
}
