// Package e2e es la suite END-TO-END del binario `enu`: la primera del repo que
// lanza el EJECUTABLE REAL como proceso (no in-process como el resto de tests).
//
// El arnés vive en los ficheros `_test.go` de este paquete (build del binario,
// workspace aislado, lanzador de proceso, provider FAKE por HTTP y helper de PTY);
// este fichero solo existe para que `go build ./...` compile un paquete no vacío
// —los `_test.go` se excluyen del build normal—. No hay superficie pública de
// producto aquí: el paquete es una batería de pruebas.
//
// Los tests que escriben ENCIMA del arnés (chat/repl/agente sobre el binario real)
// añaden sus propios `_test.go` a este mismo paquete y reutilizan los helpers.
package e2e
