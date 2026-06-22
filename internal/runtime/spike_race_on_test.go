//go:build race

package runtime

// spikeRaceEnabled detecta si la suite corre bajo `-race` (build tag `race`). El
// detector de carreras instrumenta cada acceso a memoria e infla los tiempos
// ~7x: válido para CORRECCIÓN (tests funcionales), inútil para el VETO de
// rendimiento de ADR-007, que se decide por coste de cómputo real (ver
// TestSpikeMeasureVeto). Bajo -race el veto se reporta como "indeciso".
const spikeRaceEnabled = true
