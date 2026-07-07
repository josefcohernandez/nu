package vmwasm

// Loader curado sobre wasm (migracion-vm.md M13, DM5, api.md §14). El loader es
// del KERNEL, no de la stdlib: la lib `package` de PUC NO se abre (el baseline no
// la trae). En su lugar, un `require` propio resuelve módulos por nombre contra un
// registro Go (las fuentes de los `lua/` de los plugins, leídas por Go), con
// unicidad de nombre, caché de una sola carga, detección de ciclos y reload
// best-effort (G2). Es el mismo cargador curado de hoy, portado a la frontera.
//
// Alcance (M13, mecanismo): esto es el LOADER. Cablear las 8 extensiones
// embebidas reales + el catálogo real de primitivas (fs/proc/http/...) contra las
// implementaciones Go del kernel y el compositor real es la INTEGRACIÓN con el
// Runtime (ver la descomposición de M13 en el plan). El loader es la pieza que
// deja require-ar esos módulos una vez registrados.

import (
	"fmt"
	"sort"
)

// RegisterModule registra la fuente Lua de un módulo bajo su nombre. La UNICIDAD
// de nombre es la identidad del plugin (api.md §14): un duplicado es un error de
// carga accionable. Debe llamarse antes de instanciar (el require lo resuelve en
// caliente, pero el registro se arma con el catálogo de módulos completo).
func (p *Pool) RegisterModule(name, source string) error {
	if p.modules == nil {
		p.modules = make(map[string]string)
	}
	if _, dup := p.modules[name]; dup {
		return &StructuredError{Code: "EEXIST", Message: "loader: módulo duplicado (el nombre es la identidad): " + name}
	}
	p.modules[name] = source
	return nil
}

// SetModule registra o SOBRESCRIBE la fuente Lua de un módulo, sin el chequeo de
// unicidad de RegisterModule. Es la vía del reload (G2): al recargar un plugin, sus
// módulos `lua/` se releen del disco (pueden haber cambiado) y se reemplazan aquí,
// de modo que un `require` posterior —tras vaciar la caché del preludio— sirva la
// versión nueva. No es un error de carga: el módulo ya existía y se actualiza a
// propósito.
func (p *Pool) SetModule(name, source string) {
	if p.modules == nil {
		p.modules = make(map[string]string)
	}
	p.modules[name] = source
}

// registerLoader instala la primitiva que sirve fuentes de módulo al `require` del
// preludio. Se registra en todo Pool (newBarePool) para que también los workers
// tengan require (api.md §13: las rutas del loader están disponibles en el worker).
func (p *Pool) registerLoader() {
	// nu.loader._source(name) -> source | nil. La consulta que hace require.
	p.Register("loader._source", func(inst *Instance, args []any) ([]any, error) {
		name, _ := args[0].(string)
		if src, ok := inst.pool.modules[name]; ok {
			return []any{src}, nil
		}
		return []any{nil}, nil
	})
}

// TopoOrder ordena los nombres de plugin según sus dependencias (`requires` del
// plugin.toml, api.md §14): un plugin va DESPUÉS de todo aquello de lo que
// depende. Detecta ciclos y dependencias ausentes con errores accionables. Da un
// orden de init DETERMINISTA (desempata alfabéticamente); el `require` resuelve
// las dependencias perezosamente de todas formas, pero el orden importa para el
// init con efectos (§14, el conjunto oficial va último).
func TopoOrder(deps map[string][]string) ([]string, error) {
	// Colores: 0 sin visitar, 1 en la pila (detecta ciclo), 2 hecho.
	color := make(map[string]int, len(deps))
	var order []string
	var visit func(n string, path []string) error
	visit = func(n string, path []string) error {
		switch color[n] {
		case 2:
			return nil
		case 1:
			return &StructuredError{Code: "EINVAL", Message: fmt.Sprintf("loader: ciclo de dependencias: %v -> %s", path, n)}
		}
		color[n] = 1
		reqs := append([]string(nil), deps[n]...)
		sort.Strings(reqs) // orden determinista
		for _, r := range reqs {
			if _, ok := deps[r]; !ok {
				return &StructuredError{Code: "ENOENT", Message: fmt.Sprintf("loader: %s requiere %s, que no existe", n, r)}
			}
			if err := visit(r, append(path, n)); err != nil {
				return err
			}
		}
		color[n] = 2
		order = append(order, n)
		return nil
	}
	// nombres en orden alfabético para determinismo raíz.
	names := make([]string, 0, len(deps))
	for n := range deps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}
