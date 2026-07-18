/* Trampolín de desenrollado sobre wazero — sustituye setjmp/longjmp SIN tocar
   las fuentes de Lua (los macros son definibles desde fuera, ldo.c:48).

   Cómo: LUAI_TRY no ejecuta el cuerpo inline — se lo entrega a Go
   (spike.host_try), que llama DE VUELTA al export spike_call_pfunc para
   correr (*f)(L,ud) dentro de una región recuperable. LUAI_THROW detiene la
   ejecución con un pánico Go que wazero recupera en su frontera; host_try lo
   detecta (flag + error del Call), restaura el __stack_pointer salvado y
   devuelve 1 — el equivalente exacto del retorno-por-segunda-vez de setjmp.
   El status del error ya viajó por memoria (luaD_throw escribe
   errorJmp->status ANTES de lanzar), así que el pánico no carga payload.

   Acoplamiento deliberado: usa los NOMBRES de los parámetros de
   luaD_rawrunprotected (f, ud) — único sitio de expansión de LUAI_TRY,
   estable en todo Lua 5.x. Validado por la compuerta (gate.c). */
#ifndef NU_UNWIND_H
#define NU_UNWIND_H

struct lua_State;

__attribute__((import_module("enu"), import_name("host_try")))
extern int nu_host_try(struct lua_State *L,
                          void (*f)(struct lua_State *, void *), void *ud);
__attribute__((import_module("enu"), import_name("host_throw")))
extern void nu_host_throw(void);

#define luai_jmpbuf     int  /* dummy, como en el modo C++ oficial */
#define LUAI_THROW(L,c) nu_host_throw()
#define LUAI_TRY(L,c,a) if (nu_host_try((L), (f), (ud)) == 0) { /* a corre al otro lado */ }

#endif
