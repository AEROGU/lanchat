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

// Cualquier emoji del selector debe poder enviarse en un mensaje: las
// secuencias llevan uniones, selectores y etiquetas, que no son caracteres de
// control.
func TestValidateMessageAcceptsEmoji(t *testing.T) {
	seq := func(codes ...rune) string { return string(codes) }
	for name, s := range map[string]string{
		"familia":      seq(0x1F468, 0x200D, 0x1F469, 0x200D, 0x1F467),
		"corazón rojo": seq(0x2764, 0xFE0F),
		"bandera":      seq(0x1F1F2, 0x1F1FD),
		"tecla":        seq('#', 0xFE0F, 0x20E3),
		"Inglaterra":   seq(0x1F3F4, 0xE0067, 0xE0062, 0xE0065, 0xE006E, 0xE0067, 0xE007F),
		"tono de piel": seq(0x1F44D, 0x1F3FD),
	} {
		if err := ValidateMessage("hola " + s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
