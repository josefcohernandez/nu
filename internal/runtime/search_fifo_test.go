//go:build unix

package runtime

import (
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"
)

// TestGrepCloseNoBloqueaConWorkerAtascado es el test que MATA la reversión al
// diseño bloqueante de `grepIter.close`. El rediseño (commit "cierre NO
// bloqueante") quitó el `<-it.done` de dentro de `close`: cancela el contexto,
// desregistra y vuelve, sin esperar a que el pool drene. Los tests de drenado
// (TestGrepCloseDrainaElPool, TestGrepStopAllGrepsTerminaConGrepVivo) sólo
// comprueban que `done` se sella EVENTUALMENTE; con ficheros locales pequeños
// los workers salen por `ctx.Done` en microsegundos, así que un `close`
// revertido a síncrono (`<-it.done` dentro) también volvería enseguida y esos
// tests seguirían en verde. La propiedad central —`close` NO espera al pool—
// quedaba sin blindar.
//
// Aquí la forzamos con un worker ATASCADO en una lectura NO cancelable: un
// FIFO (named pipe) dentro del árbol de búsqueda. `grepFile` hace
// `os.Open(path)`, y abrir un FIFO en O_RDONLY BLOQUEA en `open(2)` hasta que
// aparece un escritor; ni `ctx.Done` ni `cancel()` interrumpen esa llamada. Con
// el FIFO como único fichero hay un solo worker y queda colgado ahí, de modo
// que `wg.Wait()` no retorna y la cerradora nunca sella `done`.
//
// Con el diseño NO bloqueante `close` vuelve igual (sólo cancela), y lo medimos:
// falla si tarda más de 500 ms. Con la mutación (reintroducir `<-it.done` en
// `close`) la llamada colgaría para siempre en el FIFO y el test lo detecta por
// timeout de la medición. Además, justo tras retornar, comprobamos que `done`
// sigue SIN sellar (recepción no bloqueante → `default`): prueba directa de que
// `close` no esperó al pool.
func TestGrepCloseNoBloqueaConWorkerAtascado(t *testing.T) {
	// Árbol de búsqueda con un único "fichero": un FIFO. Al abrirlo O_RDONLY el
	// worker se atasca en open(2) —una lectura que la cancelación NO interrumpe—.
	root := t.TempDir()
	fifo := filepath.Join(root, "tuberia")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no se pudo crear el FIFO (plataforma sin soporte): %v", err)
	}

	re := regexp.MustCompile("NEEDLE")
	// Sin scheduler: como TestGrepCloseDrainaElPool, sólo nos importa el pool.
	// Un único fichero ⇒ un único worker ⇒ ese worker queda atascado en el Open.
	it := newGrepIter(nil, re, []string{fifo}, 0)

	// Deja que el worker arranque y llegue a `os.Open(fifo)`, donde se bloquea.
	// Si `close` cancelara antes de que el worker tomara el fichero del canal de
	// trabajo, el worker saldría por el `range work` cerrado y `done` se sellaría
	// —no ejercitaríamos el atasco—. El sleep garantiza que el worker ya está
	// dentro del `open(2)` bloqueante.
	time.Sleep(200 * time.Millisecond)

	// LIMPIEZA garantizada aunque el test falle: desbloquea el worker abriendo el
	// FIFO para escritura (con un lector presente, el open del escritor no cuelga)
	// y ciérralo enseguida → el worker ve EOF, `grepFile` retorna, la cerradora
	// sella `done` y ninguna goroutine (ni la de `close` colgada en la mutación)
	// queda viva. Se registra ANTES de arrancar la medición para que corra pase
	// lo que pase.
	t.Cleanup(func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = w.Close()
		}
		// Espera a que el pool drene de verdad: confirma que no dejamos goroutines
		// colgadas tras desbloquear el FIFO.
		select {
		case <-it.done:
		case <-time.After(2 * time.Second):
			t.Error("el pool no drenó tras desbloquear el FIFO: goroutine colgada")
		}
	})

	// Mide que `close()` RETORNA sin bloquear. Corre en una goroutine para poder
	// imponer un timeout: con el diseño bloqueante colgaría en el FIFO y el
	// `time.After` dispararía.
	returned := make(chan struct{})
	start := time.Now()
	go func() {
		it.close() // no bloqueante: cancela el contexto y vuelve
		close(returned)
	}()

	select {
	case <-returned:
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("close() tardó %v en volver: no debería esperar al pool", d)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("close() no retornó en 500ms: quedó bloqueado esperando al pool " +
			"(¿se reintrodujo `<-it.done` dentro de close?)")
	}

	// Justo tras retornar, el worker sigue atascado en el Open, luego `wg.Wait`
	// no ha vuelto y `done` NO puede estar sellado. Que lo esté significaría que
	// `close` esperó al pool antes de volver (el diseño bloqueante).
	select {
	case <-it.done:
		t.Fatal("`done` estaba sellado tras close(): close esperó a que el pool drenara")
	default:
		// Correcto: close volvió sin esperar; el pool aún no ha drenado.
	}
}
