package spike

// Pregunta 3 del spike: EL PEAJE. Los mismos programas Lua medidos en las dos
// VMs — el Lua oficial sobre wazero (lua.wasm) y gopher-lua v1.1.2 (la VM
// actual de enu). Ejes: VM pura, frontera host, throw/pcall, yield/resume,
// carga de chunk y arranque.

import (
	"context"
	"testing"

	glua "github.com/yuin/gopher-lua"
)

// --- programas compartidos --------------------------------------------------

const progFib = `
local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end
return tostring(fib(24))`

const progTablas = `
local t = {}
for i = 1, 20000 do t[i] = { v = i } end
local s = 0
for i = 1, 20000 do s = s + t[i].v end
return tostring(s)`

const progString = `
local parts = {}
for i = 1, 2000 do parts[#parts+1] = "linea " .. i end
return tostring(#table.concat(parts, "\n"))`

const progPcallThrow = `
local n = 0
for i = 1, 1000 do
  local ok = pcall(function() error("x", 0) end)
  if not ok then n = n + 1 end
end
return tostring(n)`

// wasm: host_note viene del shim; gopher: se registra equivalente abajo
const progHostCall = `
local acc = 0
for i = 1, 100000 do acc = host_note(i) end
return tostring(acc)`

const progRender = `
local chunk = string.rep("x", 1024)
local out
for i = 1, 5000 do out = host_render(chunk) end
return tostring(#out)`

// --- arneses ------------------------------------------------------------------

func benchWasm(b *testing.B, chunk, want string) {
	lw, err := NewLuaWasm("../lua.wasm")
	if err != nil {
		b.Fatal(err)
	}
	defer lw.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, lerr, err := lw.Eval(chunk)
		if err != nil || lerr != "" || out != want {
			b.Fatalf("out=%q lerr=%q err=%v", out, lerr, err)
		}
	}
}

func benchGopher(b *testing.B, chunk, want string) {
	L := glua.NewState()
	defer L.Close()
	L.SetGlobal("host_note", L.NewFunction(func(L *glua.LState) int {
		L.Push(glua.LNumber(L.CheckInt(1) + 1))
		return 1
	}))
	L.SetGlobal("host_render", L.NewFunction(func(L *glua.LState) int {
		s := L.CheckString(1)
		L.Push(glua.LString("<" + s + ">"))
		return 1
	}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := L.DoString(chunk); err != nil {
			b.Fatal(err)
		}
		got := L.Get(-1).String()
		L.Pop(1)
		if got != want {
			b.Fatalf("got %q want %q", got, want)
		}
	}
}

// --- VM pura -----------------------------------------------------------------

func BenchmarkFib_Wasm(b *testing.B)      { benchWasm(b, progFib, "46368") }
func BenchmarkFib_Gopher(b *testing.B)    { benchGopher(b, progFib, "46368") }
func BenchmarkTablas_Wasm(b *testing.B)   { benchWasm(b, progTablas, "200010000") }
func BenchmarkTablas_Gopher(b *testing.B) { benchGopher(b, progTablas, "200010000") }
func BenchmarkString_Wasm(b *testing.B)   { benchWasm(b, progString, "20892") }
func BenchmarkString_Gopher(b *testing.B) { benchGopher(b, progString, "20892") }

// --- throw a través del trampolín vs nativo -----------------------------------

func BenchmarkPcallThrow_Wasm(b *testing.B)   { benchWasm(b, progPcallThrow, "1000") }
func BenchmarkPcallThrow_Gopher(b *testing.B) { benchGopher(b, progPcallThrow, "1000") }

// --- frontera host -------------------------------------------------------------

func BenchmarkHostCall_Wasm(b *testing.B)   { benchWasm(b, progHostCall, "100001") }
func BenchmarkHostCall_Gopher(b *testing.B) { benchGopher(b, progHostCall, "100001") }
func BenchmarkRender_Wasm(b *testing.B)     { benchWasm(b, progRender, "1026") }
func BenchmarkRender_Gopher(b *testing.B)   { benchGopher(b, progRender, "1026") }

// --- yield/resume (el puente ⏸) -----------------------------------------------

func BenchmarkYieldResume_Wasm(b *testing.B) {
	lw, err := NewLuaWasm("../lua.wasm")
	if err != nil {
		b.Fatal(err)
	}
	defer lw.Close()
	ref, err := lw.CoSpawn(`while true do nu_await("t") end`)
	if err != nil {
		b.Fatal(err)
	}
	v := "v"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, _, err := lw.CoResume(ref, &v)
		if err != nil || st != CoYield {
			b.Fatalf("st=%v err=%v", st, err)
		}
	}
}

func BenchmarkYieldResume_Gopher(b *testing.B) {
	L := glua.NewState()
	defer L.Close()
	fn, err := L.LoadString(`while true do coroutine.yield("t") end`)
	if err != nil {
		b.Fatal(err)
	}
	co, _ := L.NewThread()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st, err2, _ := L.Resume(co, fn, glua.LString("v"))
		if st != glua.ResumeYield || err2 != nil {
			b.Fatalf("st=%v err=%v", st, err2)
		}
	}
}

// --- arranque -------------------------------------------------------------------

func BenchmarkArranque_Wasm(b *testing.B) {
	for i := 0; i < b.N; i++ {
		lw, err := NewLuaWasm("../lua.wasm")
		if err != nil {
			b.Fatal(err)
		}
		lw.Close()
	}
}

func BenchmarkArranque_Gopher(b *testing.B) {
	for i := 0; i < b.N; i++ {
		L := glua.NewState()
		L.Close()
	}
}

var _ = context.Background
