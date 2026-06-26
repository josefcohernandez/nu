package runtime

// Pantalla de runtime desnudo (api.md §14, G21, S33). Cuando nu arranca con un
// TTY interactivo y NINGÚN plugin activo (ni de usuario ni embebido activado por
// `nu.toml`), el kernel pinta —ANTES de correr Lua de producto— una pantalla FIJA
// hecha SOLO de sus propias capacidades: la versión y el nivel de API
// (`nu.version`), las rutas de config y de plugins (`nu.config.dir` y los
// directorios de plugins), el catálogo de extensiones embebidas DISPONIBLES
// (`embeddedNames`, embed.go) y las ACCIONES que ofrece. No es la UI de un
// producto sino la del propio runtime: las extensiones embebidas y su activación
// son capacidad del loader, así que el kernel habla de lo suyo (filosofia.md §2).
// Render FIJO (celdas/Block sobre el compositor de S29), pre-Lua, sin widgets ni
// lógica de producto. Es lo que se ve SIEMPRE que nu arranca sin nada activo, no
// un diálogo de primera vez.
//
// CONDICIÓN (§14): se muestra SSI hay superficie de UI (`rt.uiActive`: un TTY
// interactivo, o `WithForceUI` en test) Y no hay plugins activos. Sin TTY NO se
// pinta nada: el runtime arranca "desnudo" (Boot normal) y los errores por
// extensión inactiva siguen siendo accionables (nombran la línea de `nu.toml`,
// S12). Con cualquier plugin activo tampoco se pinta: el arranque sigue su curso.
//
// ACCIONES (§14): (1) activar el CONJUNTO oficial de producto → escribe `plugins.enabled`
// en `config.dir()/nu.toml` con las extensiones embebidas del conjunto de producto (todas
// menos el andamiaje `example`, ADR-015) y CONTINÚA el arranque canónico (`Boot`), SIN red
// (la activación de una embebida sale del binario, ADR-010); (2) activar extensiones SUELTAS
// (p. ej. solo `repl`) → escribe solo esas; (3) salir. La elección real con el TECLADO usa el input de S31 + el
// driver de TTY; en este entorno HEADLESS no hay TTY, así que la lógica
// "activar → escribir nu.toml → continuar Boot" se expone por una vía interna
// (`activateAndBoot`) testeable, y el render se inspecciona componiendo a un buffer
// (la rejilla del compositor / su salida ANSI).
//
// FRONTERA. La pantalla NO añade superficie Lua nueva (es del kernel, pre-Lua, §14
// ya describe G21): no toca `api.md` ni `nu.version.api`. La interacción de teclado
// visible, el streaming visible y el resize/paste visibles son el CP-7 MANUAL con
// TTY (no ejecutable en CI headless): aquí se cubre lo automatizable (el render a
// buffer, la condición TTY+sin-plugins, y activar→nu.toml→Boot).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// BareScreenActive es la cara pública de `bareScreenActive` para `main` (el binario):
// indica si, con la config y el entorno actuales, toca pintar la pantalla de runtime
// desnudo (hay TTY y ningún plugin activo). `main` la consulta tras `New` para decidir
// entre pintar la pantalla (TTY interactivo, sin plugins) y el arranque normal (`Boot`).
func (rt *Runtime) BareScreenActive() bool { return rt.bareScreenActive() }

// RenderBareScreen es la cara pública de `renderBareScreen` para `main`: pinta la
// pantalla de runtime desnudo en el compositor. Devuelve las líneas FIJAS mostradas
// (versión, rutas, embebidas, acciones) para que `main` las pueda volcar al terminal
// mientras el driver de TTY (S33+) que lee el teclado para elegir una acción no esté
// cableado. La interacción real (elegir con el teclado, ver el efecto) es el CP-7
// MANUAL con TTY.
func (rt *Runtime) RenderBareScreen() []string {
	return rt.renderBareScreen().lines()
}

// ActivateOfficial activa el CONJUNTO oficial de producto (las embebidas menos el
// andamiaje `example`, ADR-015) y continúa el arranque (§14): es la primera acción de
// la pantalla desnuda. Sin red (las embebidas salen del binario, ADR-010). La invoca
// la elección de teclado "activar el conjunto oficial" (driver de TTY, S33+); el flag
// `nu --default-config` (sin TTY, G33) activa el MISMO conjunto vía `officialProductSet`,
// de modo que pantalla y flag enchufan lo mismo.
func (rt *Runtime) ActivateOfficial() error {
	names, err := officialProductSet()
	if err != nil {
		return err
	}
	return rt.activateAndBoot(names)
}

// OfficialProductSet expone el conjunto oficial de producto (ADR-015, G33) a `main`
// (el binario) como FUNCIÓN DE PAQUETE —no método—: el modo EFÍMERO de
// `nu --default-config` necesita el conjunto ANTES de construir el Runtime (para
// pasarlo a `WithEnabledPlugins`), cuando aún no hay `rt`. El conjunto es estático
// (sale del `embed.FS`, sin estado de runtime), así que no requiere un Runtime. Es un
// wrapper fino de `officialProductSet`.
func OfficialProductSet() ([]string, error) { return officialProductSet() }

// WriteDefaultConfig respalda el modo PERSISTENTE de `nu --default-config` (ADR-015,
// G33): escribe el conjunto oficial de producto en `plugins.enabled` de
// `config.dir()/nu.toml` —preservando el resto del fichero, atómico, idempotente; un
// `nu.toml` mal formado NO se sobrescribe (error accionable)— y devuelve `(configDir,
// names, err)` para que `main` informe qué escribió y dónde. NO arranca nada (a
// diferencia de `ActivateOfficial`, la acción TTY que escribe Y continúa el `Boot`):
// el modo persistente escribe y sale. Sin red (ADR-010).
func (rt *Runtime) WriteDefaultConfig() (configDir string, names []string, err error) {
	names, err = officialProductSet()
	if err != nil {
		return "", nil, err
	}
	if err = writeEnabledPlugins(rt.ldr.configDir, names); err != nil {
		return "", nil, err
	}
	return rt.ldr.configDir, names, nil
}

// bareScreenActive decide si toca pintar la pantalla de runtime desnudo (§14, G21):
// hay superficie de UI (`rt.uiActive`) Y no hay plugins activos. Es el gate que
// `Boot` consulta antes de cargar Lua de producto. Sin UI (headless) o con algún
// plugin activo, devuelve false y el arranque sigue normal.
func (rt *Runtime) bareScreenActive() bool {
	return rt.uiActive && !rt.ldr.hasActivePlugins()
}

// hasActivePlugins informa, ANTES de correr ningún `init.lua`, si el arranque
// cargaría algún plugin: o bien `plugins.enabled` de `nu.toml` nombra algo (una
// embebida activada, ADR-010), o bien algún directorio de plugins contiene un
// plugin de disco. Es deliberadamente LIGERO —no materializa embebidas ni valida el
// grafo (eso lo hace `discover`/`topoSort` en el `Boot` real)—: solo decide si la
// pantalla desnuda procede. Una config rota (`configErr`) no se trata aquí; `Boot`
// la devolverá igual antes de pintar nada.
func (l *loader) hasActivePlugins() bool {
	if len(l.enabled) > 0 {
		return true
	}
	return l.anyDiskPlugin()
}

// anyDiskPlugin devuelve true en cuanto encuentra UN subdirectorio con `plugin.toml`
// en cualquiera de los directorios de plugins configurados (`WithPluginDir` +
// `plugins.dirs`). No parsea el manifiesto ni valida nada: para decidir si hay
// plugins de disco basta su presencia. Un directorio inexistente o ilegible no
// aporta (no es fatal aquí: el `Boot` real reporta los errores de IO).
func (l *loader) anyDiskPlugin() bool {
	for _, root := range l.pluginDirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			manifest := filepath.Join(root, e.Name(), pluginManifestName)
			if _, err := os.Stat(manifest); err == nil {
				return true
			}
		}
	}
	return false
}

// bareScreenModel reúne lo que la pantalla desnuda muestra (§14): es el estado FIJO
// derivado solo de las capacidades del kernel. Lo construye `buildBareScreenModel`
// y lo consume `renderBareScreen` (para pintar) y los tests (para verificar el
// contenido sin depender del layout exacto).
type bareScreenModel struct {
	versionLine string   // "nu 0.1.0 · API 2"
	configDir   string   // config.dir()
	pluginDirs  []string // directorios donde se buscan plugins
	embedded    []string // catálogo de extensiones embebidas DISPONIBLES
	actions     []string // las acciones ofrecidas (activar conjunto / sueltas / salir)
}

// buildBareScreenModel arma el modelo fijo de la pantalla desnuda a partir de las
// capacidades del runtime: versión + nivel de API (§2), rutas (§14), catálogo de
// embebidas (embed.go) y acciones (§14). No toca disco salvo enumerar el catálogo
// embebido (que sale del binario, sin red). Un fallo al enumerar el catálogo deja la
// lista vacía: la pantalla se pinta igual (el catálogo es informativo).
func (rt *Runtime) buildBareScreenModel() bareScreenModel {
	embedded, _ := embeddedNames() // del binario (ADR-010); si falla, lista vacía

	m := bareScreenModel{
		versionLine: fmt.Sprintf("nu %d.%d.%d · API %d",
			VersionMajor, VersionMinor, VersionPatch, APILevel),
		configDir:  rt.ldr.configDir,
		pluginDirs: append([]string(nil), rt.ldr.pluginDirs...),
		embedded:   embedded,
		actions: []string{
			"activar el conjunto oficial",
			"activar extensiones sueltas (p. ej. solo repl)",
			"salir",
		},
	}
	return m
}

// bareScreenLines produce las líneas de texto FIJAS de la pantalla desnuda, en
// orden. Es texto plano (sin estilos): el render es del runtime, no de un producto.
// Lo usa `renderBareScreen` (para blittear un Block) y los tests (para comprobar que
// las cadenas esperadas —versión, rutas, embebidas, acciones— están presentes).
func (m bareScreenModel) lines() []string {
	var ls []string
	ls = append(ls, "nu — runtime desnudo")
	ls = append(ls, "")
	ls = append(ls, m.versionLine)
	ls = append(ls, "")
	ls = append(ls, "config: "+m.configDir)
	if len(m.pluginDirs) == 0 {
		ls = append(ls, "plugins: (ninguno configurado)")
	} else {
		for i, d := range m.pluginDirs {
			if i == 0 {
				ls = append(ls, "plugins: "+d)
			} else {
				ls = append(ls, "         "+d)
			}
		}
	}
	ls = append(ls, "")
	if len(m.embedded) == 0 {
		ls = append(ls, "extensiones embebidas: (ninguna)")
	} else {
		ls = append(ls, "extensiones embebidas disponibles:")
		for _, name := range m.embedded {
			ls = append(ls, "  - "+name)
		}
	}
	ls = append(ls, "")
	ls = append(ls, "acciones:")
	for i, a := range m.actions {
		ls = append(ls, fmt.Sprintf("  %d) %s", i+1, a))
	}
	return ls
}

// renderBareScreen compone la pantalla desnuda en el compositor (§9.1, S29) y la
// pinta: construye un Block fijo con las líneas del modelo, lo blittea en una región
// a pantalla completa y fuerza un `paint`. Devuelve el modelo pintado (para que el
// llamante/tests sepan qué se mostró). Corre bajo el token, en el estado principal
// (lo invoca `Boot`, que lo tiene): toca el compositor como cualquier mutación de
// `nu.ui`. Requiere `rt.ui != nil` (garantizado por el gate `bareScreenActive`, que
// exige `uiActive`).
//
// El render es FIJO y pre-Lua: no hay widgets ni lógica de producto, solo celdas. El
// resultado vive en la rejilla del compositor (`back`) y en su salida ANSI
// (`encoded`), ambas inspeccionables por los tests sin un TTY real.
func (rt *Runtime) renderBareScreen() bareScreenModel {
	m := rt.buildBareScreenModel()
	if rt.ui == nil {
		return m // defensivo: el gate ya exige uiActive, pero no asumimos compositor
	}

	lines := m.lines()
	spanLines := make([][]span, len(lines))
	for i, ln := range lines {
		spanLines[i] = []span{{text: ln}}
	}
	b := newBlock(spanLines)

	comp := rt.ui.comp
	// Una región a pantalla completa, sin dueño de plugin (es del runtime): el
	// owner "user" la etiqueta como cualquier handle del estado principal. Se
	// blittea el Block en su origen (0,0) y se compone+pinta de inmediato (no se
	// espera al timer de coalescing: la pantalla desnuda debe verse ya).
	r := comp.addRegion(0, 0, comp.w, comp.h, 0, ownerUser)
	r.content.blitBlock(0, 0, b)
	comp.markDirty()
	comp.paint()
	return m
}

// activateAndBoot es la lógica de la ACCIÓN de la pantalla desnuda (§14): escribe
// `names` en `plugins.enabled` de `config.dir()/nu.toml` (preservando el resto del
// fichero si existía) y CONTINÚA el arranque canónico (`Boot`), SIN red. Es la vía
// INTERNA y testeable de "activar → escribir nu.toml → continuar Boot": en
// producción la dispara la elección de teclado (driver de TTY, S33+); en headless la
// invocan los tests.
//
//   - "activar el conjunto oficial" = `activateAndBoot(officialProductSet())` (las
//     embebidas del catálogo menos el andamiaje `example`, ADR-015).
//   - "activar suelta" = `activateAndBoot([]string{"repl"})` (solo esa).
//
// Tras escribir el fichero, recarga la config (`plugins.enabled`/`dirs`/watchdog) en
// el loader y arranca: así el `Boot` posterior carga las recién activadas con
// `source="builtin"`, exactamente como si el usuario hubiera editado `nu.toml` a
// mano y vuelto a arrancar (ADR-010). Devuelve el error de `Boot` (grafo inválido,
// config rota...), accionable.
func (rt *Runtime) activateAndBoot(names []string) error {
	if err := writeEnabledPlugins(rt.ldr.configDir, names); err != nil {
		return err
	}
	// Releer la config tras escribirla: el loader debe ver la nueva `plugins.enabled`
	// (y cualquier `dirs`/watchdog que ya hubiera). Reseteamos `booted` para que el
	// `Boot` que sigue cargue de verdad (la pantalla desnuda no llegó a cargar nada).
	nuCfg, _, tomlErr := loadNuToml(rt.ldr.configDir)
	rt.ldr.enabled = nuCfg.Plugins.Enabled
	rt.ldr.configErr = tomlErr
	rt.ldr.booted = false
	// `rt.Boot()` (no `ldr.Boot()`) para que, además de cargar los plugins recién
	// activados, se arme el timer de coalescing del compositor (`armPainter`,
	// idempotente): tras activar, la UI de las extensiones debe repintarse sola.
	return rt.Boot()
}

// writeEnabledPlugins escribe (o actualiza) `plugins.enabled` en
// `config.dir()/nu.toml`, PRESERVANDO el resto del fichero si existe (otras claves
// de `[plugins]`, `[watchdog]`, `[net]`, claves desconocidas...). La estrategia: leer
// el TOML existente a un mapa genérico, fijar `plugins.enabled`, y reescribir todo
// con la librería TOML pura-Go (BurntSushi, la misma del loader, S11) de forma
// ATÓMICA (escribir a un temporal y renombrar) para no dejar un `nu.toml` a medias si
// el proceso muere a mitad. Un fichero ausente se crea con solo esa clave.
//
// POR QUÉ un mapa genérico y no `runtimeConfig`: re-serializar `runtimeConfig`
// perdería las claves que el core ignora por forward-compat (config_toml.go), y la
// pantalla desnuda no debe pisar configuración del usuario que no entiende. Un mapa
// `map[string]any` round-trippea todo lo que BurntSushi parseó.
func writeEnabledPlugins(configDir string, names []string) error {
	path := filepath.Join(configDir, nuTomlName)

	// Lee el TOML existente a un mapa genérico (preserva claves desconocidas). Un
	// fichero ausente arranca de un mapa vacío; uno mal formado es un error
	// accionable (no lo sobrescribimos a ciegas: perderíamos config del usuario).
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if _, decErr := toml.Decode(string(data), &root); decErr != nil {
			return &StructuredError{Code: CodeEINVAL,
				Message: fmt.Sprintf("%s inválido en %q: %v (no se sobrescribe para no perder tu configuración; corrígelo a mano)",
					nuTomlName, path, decErr)}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo leer %q: %v", path, err)}
	}

	// Fija `plugins.enabled`, conservando el resto de `[plugins]` si existía.
	plugins, _ := root["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}
	enabled := make([]any, len(names))
	for i, n := range names {
		enabled[i] = n
	}
	plugins["enabled"] = enabled
	root["plugins"] = plugins

	// Serializa y escribe atómicamente (temporal + rename) bajo `config.dir()`. El
	// directorio de config debe existir; si no, se crea (primer arranque del usuario).
	// La escritura reusa `writeAtomic` (S14): temporal en el mismo directorio +
	// `rename`, para no dejar un `nu.toml` a medias si el proceso muere a mitad.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo crear el directorio de configuración %q: %v", configDir, err)}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(root); err != nil {
		return &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo serializar %s: %v", nuTomlName, err)}
	}
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return &StructuredError{Code: CodeEIO,
			Message: fmt.Sprintf("no se pudo escribir %q: %v", path, err)}
	}
	return nil
}
