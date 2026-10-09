package transfer

import (
	"sync"

	"github.com/AEROGU/lanchat/internal/store"
	"github.com/AEROGU/lanchat/internal/thumb"
)

// thumbWorkers: imágenes que se reducen a la vez al ofrecer archivos.
const thumbWorkers = 4

// addThumbs genera en memoria la miniatura de las primeras imágenes de una
// oferta (thumb.MaxPerOffer), para que quien la recibe las vea antes de
// aceptar. Las que no se pueden leer quedan sin miniatura.
func addThumbs(files []store.TransferFile) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, thumbWorkers)
	n := 0
	for i := range files {
		if n == thumb.MaxPerOffer {
			break
		}
		if !thumb.Supported(files[i].Name) {
			continue
		}
		n++
		wg.Add(1)
		sem <- struct{}{}
		go func(f *store.TransferFile) {
			defer func() { <-sem; wg.Done() }()
			f.Thumb = safeThumb(f.Path)
		}(&files[i])
	}
	wg.Wait()
}

// safeThumb es la miniatura de path, o nil si no se pudo. Un archivo recibido
// viene de otro equipo: un decodificador que entre en pánico no debe tumbar
// LanChat.
func safeThumb(path string) (b []byte) {
	defer func() {
		if recover() != nil {
			b = nil
		}
	}()
	b, err := thumb.FromFile(path)
	if err != nil {
		return nil
	}
	return b
}
