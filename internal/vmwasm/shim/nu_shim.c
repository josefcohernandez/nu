/* Shim productivo de nu.wasm (migracion-vm.md M02): la superficie C que el
 * kernel Go ve del estado Lua compilado a WebAssembly. Promueve el shim del
 * spike (spike/lua-wasm/shim/lua_shim.c) a calidad de kernel:
 *
 *   - libs SEGURAS del baseline (base/table/string/math/coroutine/utf8 — sin
 *     io/os/package/debug, api.md §1.2; el recorte fino de `os` y globales lo
 *     hace el preludio Lua del sandbox, M04);
 *   - protocolo de intercambio por buffer compartido (nu_buf/nu_result_len):
 *     el kernel escribe args ahí, llama un export, lee el resultado ahí;
 *   - eval protegido (nu_eval) y corrutinas (nu_co_*) para el puente ⏸ (M06);
 *   - dispatch host GENÉRICO (nu_host_dispatch): la única costura de llamada
 *     Lua→Go, sobre la que M05 construye el marshaling y todas las primitivas
 *     `nu.*`. Sustituye a los host functions hardcodeados del spike
 *     (host_note/host_render, que eran de benchmark);
 *   - el callback del trampolín de desenrollado (nu_call_pfunc, nu_unwind.h).
 *
 * MULTI-INSTANCIA (DM3/M12): el estado (GL) y el buffer (BUF) son variables de
 * la memoria lineal del módulo. wazero da una memoria lineal NUEVA por cada
 * instanciación del módulo compilado, así que N instancias (N workers) tienen
 * cada una su GL y su BUF sin compartir nada — el aislamiento físico que
 * ADR-019 promete sale gratis del modelo de WASM.
 */

#include <string.h>
#include <stdlib.h>
#include "lua.h"
#include "lauxlib.h"
#include "lualib.h"

/* Estado Lua de ESTA instancia (una por módulo wazero instanciado). */
static lua_State *GL = NULL;

/* Buffer de intercambio Go<->wasm de esta instancia. 256 KiB cubre chunks y
 * resultados corrientes; los payloads mayores se trocean (M05 define el
 * protocolo de continuación si hiciera falta). */
#define BUF_CAP (256 * 1024)
static char BUF[BUF_CAP];
static int RESULT_LEN = 0;

__attribute__((export_name("nu_buf")))
char *nu_buf(void) { return BUF; }

__attribute__((export_name("nu_buf_cap")))
int nu_buf_cap(void) { return BUF_CAP; }

__attribute__((export_name("nu_result_len")))
int nu_result_len(void) { return RESULT_LEN; }

/* set_result copia al buffer la representación string de la pila en `idx`
 * (respeta __tostring vía luaL_tolstring) y deja su longitud en RESULT_LEN. */
static void set_result(lua_State *L, int idx) {
  size_t n = 0;
  const char *s = luaL_tolstring(L, idx, &n);
  if (n > BUF_CAP - 1) n = BUF_CAP - 1;
  memcpy(BUF, s, n);
  BUF[n] = 0;
  RESULT_LEN = (int)n;
  lua_pop(L, 1); /* el string que dejó luaL_tolstring */
}

/* set_result_raw copia `len` bytes crudos del propio BUF (idempotente: el
 * dispatch host ya los escribió ahí) y fija RESULT_LEN. */
static void set_result_len(int len) {
  if (len < 0) len = 0;
  if (len > BUF_CAP - 1) len = BUF_CAP - 1;
  BUF[len] = 0;
  RESULT_LEN = len;
}

/* --- trampolín de desenrollado (nu_unwind.h) --------------------------------
 * El callback que Go re-entra para correr el cuerpo protegido de LUAI_TRY. */
typedef void (*pfunc_t)(lua_State *, void *);
__attribute__((export_name("nu_call_pfunc")))
void nu_call_pfunc(lua_State *L, pfunc_t f, void *ud) { f(L, ud); }

/* --- dispatch host genérico -------------------------------------------------
 * La ÚNICA costura de llamada Lua→Go. Go recibe (id, len): lee los `len` bytes
 * de args en BUF, ejecuta la primitiva `id`, escribe el resultado en BUF y
 * devuelve su longitud (>=0) o un negativo -(code) para señalar error. M05
 * define el formato de args/resultado (marshaling) y el catálogo de ids. */
__attribute__((import_module("nu"), import_name("host_dispatch")))
extern int nu_host_dispatch(int id, int len);

/* __nu_host(id, argstr) -> (ok, resultstr): el puente Lua sobre el dispatch.
 * El preludio de M05 envuelve cada primitiva `nu.*` en un thunk que llama a
 * esta función registrada. Aquí sólo mueve bytes: copia argstr a BUF, invoca
 * el dispatch, y devuelve el resultado como string (con un bool de ok/err para
 * que el thunk decida entre retornar o `error(...)`). */
static int l_nu_host(lua_State *L) {
  int id = (int)luaL_checkinteger(L, 1);
  size_t n = 0;
  const char *s = luaL_optlstring(L, 2, "", &n);
  if (n > BUF_CAP - 1) n = BUF_CAP - 1;
  memcpy(BUF, s, n);
  int r = nu_host_dispatch(id, (int)n);
  if (r < 0) {
    set_result_len(-r > BUF_CAP - 1 ? 0 : 0); /* el mensaje de error va en BUF */
    /* En error, Go deja el mensaje estructurado (JSON) en BUF con longitud en
     * el valor absoluto NO — el protocolo de M05 lo fija; aquí devolvemos el
     * contenido de BUF tal cual con la longitud que Go quiera exponer vía un
     * segundo dispatch. Para M02, error = (false, ""); M05 lo detalla. */
    lua_pushboolean(L, 0);
    lua_pushlstring(L, BUF, (size_t)RESULT_LEN);
    return 2;
  }
  set_result_len(r);
  lua_pushboolean(L, 1);
  lua_pushlstring(L, BUF, (size_t)RESULT_LEN);
  return 2;
}

/* nu_await(...): la primitiva ⏸ — yield-ea al lado Go; los valores del resume
 * se vuelven sus valores de retorno. Es la costura del puente (M06). */
static int l_await(lua_State *L) {
  return lua_yield(L, lua_gettop(L));
}

/* --- watchdog por conteo de instrucciones (DM4) -----------------------------
 * El watchdog del backend wasm. En wazero cancelar el ctx de un Call mata el
 * MÓDULO ENTERO (el estado), no una task; para abortar SÓLO la task que quema CPU
 * (`while true do end`) sin matar el estado, la única palanca es el propio bucle
 * del intérprete: un count-hook de PUC-Lua que, cada N instrucciones, pregunta a
 * Go si el slice en curso rebasó su presupuesto y, si es así, CEDE.
 *
 * VERIFICADO EN FASE 0: un count-hook de Lua 5.4 SÍ puede ceder a través del
 * trampolín Snapshot/Restore (M03) y de un pcall —el yield del hook es un
 * luaD_throw(LUA_YIELD) idéntico al de un coroutine.yield normal, que ya
 * atraviesa el pcall (base yieldable, CIST_YPCALL) sin frame de try intermedio—.
 * Por eso el aborto es NO capturable: el pcall del usuario nunca lo ve.
 *
 * SIN VALORES: un count-hook NO puede ceder valores —luaD_hook restaura el `top`
 * de la pila tras ejecutar el hook, así que cualquier valor empujado se pierde—.
 * Por eso `lua_yield(L, 0)`: el scheduler Lua reconoce el aborto por budget
 * porque coroutine.resume devuelve un yield con `yielded == nil` (todos los ⏸
 * normales ceden una tabla {op=...}, jamás nil). */

/* nu_over_budget(): import host (Go). Devuelve 1 si el slice de la task en curso
 * rebasó su deadline (fijado por __reset_budget antes de reanudar la task). Es
 * race-free: mismo goroutine que conduce el Call, invocación síncrona. */
__attribute__((import_module("nu"), import_name("nu_over_budget")))
extern int nu_over_budget(void);

/* WD_COUNT: instrucciones entre chequeos. Compromiso entre granularidad del corte
 * (rebasar el deadline como mucho ~WD_COUNT instrucciones) y coste (una llamada
 * host por cada tramo). El chequeo es barato (una comparación de tiempos en Go). */
#define WD_COUNT 10000

/* wd_hook: el count-hook. Cada WD_COUNT instrucciones pregunta a Go; si el slice
 * rebasó el presupuesto, cede (0 valores) — el scheduler lo aborta con EBUDGET. */
static void wd_hook(lua_State *L, lua_Debug *ar) {
  (void)ar;
  if (nu_over_budget()) {
    lua_yield(L, 0);
  }
}

/* __wd_arm(): instala wd_hook en el HILO que la llama (la corrutina de la task).
 * El preludio la invoca al principio del cuerpo de cada task. Como el baseline NO
 * abre la lib debug, esta función C global es la única vía de lua_sethook; se
 * instala por-corrutina (un hilo nuevo NO hereda el hook del padre en 5.4). */
static int l_wd_arm(lua_State *L) {
  lua_sethook(L, wd_hook, LUA_MASKCOUNT, WD_COUNT);
  return 0;
}

/* --- ciclo de vida del estado ---------------------------------------------- */

__attribute__((export_name("nu_new")))
int nu_new(void) {
  GL = luaL_newstate();
  if (!GL) return 1;
  luaL_requiref(GL, LUA_GNAME, luaopen_base, 1);          lua_pop(GL, 1);
  luaL_requiref(GL, LUA_TABLIBNAME, luaopen_table, 1);    lua_pop(GL, 1);
  luaL_requiref(GL, LUA_STRLIBNAME, luaopen_string, 1);   lua_pop(GL, 1);
  luaL_requiref(GL, LUA_MATHLIBNAME, luaopen_math, 1);    lua_pop(GL, 1);
  luaL_requiref(GL, LUA_COLIBNAME, luaopen_coroutine, 1); lua_pop(GL, 1);
  luaL_requiref(GL, LUA_UTF8LIBNAME, luaopen_utf8, 1);    lua_pop(GL, 1);
  /* costuras del kernel (no son de la stdlib): el dispatch host y el ⏸ */
  lua_register(GL, "__nu_host", l_nu_host);
  lua_register(GL, "nu_await", l_await);
  lua_register(GL, "__wd_arm", l_wd_arm); /* watchdog DM4: instala el count-hook */
  return 0;
}

/* nu_eval(len): carga+corre BUF[0..len] protegido. 0 ok (resultado en BUF si
 * el chunk devolvió algo no-nil), 2 error (mensaje en BUF). */
__attribute__((export_name("nu_eval")))
int nu_eval(int len) {
  RESULT_LEN = 0;
  if (luaL_loadbuffer(GL, BUF, (size_t)len, "chunk") != LUA_OK) {
    set_result(GL, -1); lua_pop(GL, 1); return 2;
  }
  if (lua_pcall(GL, 0, 1, 0) != LUA_OK) {
    set_result(GL, -1); lua_pop(GL, 1); return 2;
  }
  if (!lua_isnil(GL, -1)) set_result(GL, -1);
  lua_pop(GL, 1);
  return 0;
}

/* nu_sched_step(len): el puente Go↔bucle de scheduler (ADR-020, M06). Pasa
 * BUF[0..len] (los resultados de trabajo externo ya completado, wire de M05) al
 * scheduler Lua global `__sched_step`, y deja en BUF lo que devuelve (las nuevas
 * peticiones de trabajo externo pendientes, wire). Devuelve la longitud del
 * retorno, o -1 si `__sched_step` no existe o falla (mensaje en BUF). Corre bajo
 * el pcall que `__sched_step` establece; los errores de las tasks no escapan aquí
 * (el scheduler Lua los captura por task). */
__attribute__((export_name("nu_sched_step")))
int nu_sched_step(int len) {
  lua_getglobal(GL, "__sched_step");
  if (!lua_isfunction(GL, -1)) { lua_pop(GL, 1); RESULT_LEN = 0; return -1; }
  lua_pushlstring(GL, BUF, (size_t)len);
  if (lua_pcall(GL, 1, 1, 0) != LUA_OK) { set_result(GL, -1); lua_pop(GL, 1); return -1; }
  size_t n = 0;
  const char *s = lua_tolstring(GL, -1, &n);
  if (n > BUF_CAP - 1) n = BUF_CAP - 1;
  if (s) memcpy(BUF, s, n);
  RESULT_LEN = (int)n;
  lua_pop(GL, 1);
  return (int)n;
}

/* nu_selftest_trap: provoca un TRAP wasm real (no un error de Lua). Lo usa el
 * test 🔒 de M03 para verificar que el trampolín distingue un trap del motor
 * (que DEBE propagarse como fallo duro a Go) de un LUAI_THROW (que se captura).
 * No forma parte de la superficie del kernel; existe para blindar esa frontera. */
__attribute__((export_name("nu_selftest_trap")))
void nu_selftest_trap(void) { __builtin_trap(); }

/* --- corrutinas (el puente ⏸, M06) ----------------------------------------- */

/* crea una corrutina desde BUF[0..len]; la ancla en el registry y devuelve su
 * ref (>0) o -1 si no compila (mensaje en BUF). */
__attribute__((export_name("nu_co_spawn")))
int nu_co_spawn(int len) {
  lua_State *co = lua_newthread(GL);
  if (luaL_loadbuffer(co, BUF, (size_t)len, "co") != LUA_OK) {
    set_result(co, -1);
    lua_pop(GL, 1);
    return -1;
  }
  return luaL_ref(GL, LUA_REGISTRYINDEX);
}

/* resume con un string opcional (len<0 = sin argumento). 0 done, 1 yield, 2
 * error; resultado/yield/error en BUF. Al terminar (done/error) libera la ref. */
__attribute__((export_name("nu_co_resume")))
int nu_co_resume(int ref, int len) {
  lua_rawgeti(GL, LUA_REGISTRYINDEX, ref);
  lua_State *co = lua_tothread(GL, -1);
  lua_pop(GL, 1);
  int nargs = 0;
  if (len >= 0) { lua_pushlstring(co, BUF, (size_t)len); nargs = 1; }
  int nres = 0;
  int st = lua_resume(co, GL, nargs, &nres);
  RESULT_LEN = 0;
  if (st == LUA_YIELD) {
    if (nres > 0) set_result(co, -1);
    lua_pop(co, nres);
    return 1;
  }
  if (st == LUA_OK) {
    if (nres > 0) set_result(co, -1);
    lua_pop(co, nres);
    luaL_unref(GL, LUA_REGISTRYINDEX, ref);
    return 0;
  }
  set_result(co, -1);
  luaL_unref(GL, LUA_REGISTRYINDEX, ref);
  return 2;
}
