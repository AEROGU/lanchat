package i18n

import "golang.org/x/sys/windows"

// systemLanguages devuelve los idiomas de la interfaz de Windows en orden de
// preferencia ("es-MX", "en-US"…).
func systemLanguages() []string {
	tags, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}
	return tags
}
