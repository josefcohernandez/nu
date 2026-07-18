// Panel de jueces clean-room para el proyecto nu — la versión determinista de
// la skill juicio/ (léela: .claude/skills/juicio/SKILL.md es el contrato; este
// script solo lo mecaniza). Úsalo para el panel completo de una sesión 🔒 o
// una auditoría grande; para diffs menores basta la skill.
//
// Invocación (desde el hilo principal, con el diff YA preparado):
//   Workflow({ name: 'revision-limpia', args: {
//     sesion: 'S## — <enunciado verbatim del plan>',
//     espec:  'api.md §4.2, agente.md §3 (+ G27)',
//     diff:   '<salida completa de git diff, verbatim>',
//     mutantes: '<informe LIVED de /mutacion, opcional — solo para juez-tests>',
//   }})
//
// No-contaminación: el prompt de cada juez se construye SOLO con args
// (sesión + espec + diff). Nunca añadas al args razonamiento del autor,
// alternativas discutidas ni contenido de la bitácora.

export const meta = {
  name: 'revision-limpia',
  description: 'Panel de jueces clean-room (espec, tests, concurrencia) con verificación adversarial por hallazgo',
  whenToUse: 'Cierre de sesión 🔒 o diff que toca api.md/contratos/scheduler; para diffs menores usa la skill juicio directamente',
  phases: [
    { title: 'Juicio', detail: 'tres jueces en paralelo, contexto limpio' },
    { title: 'Verificación', detail: 'un verificador fresco por hallazgo' },
  ],
}

const VEREDICTO_JUEZ = {
  type: 'object',
  required: ['conforme', 'hallazgos', 'caminos_intentados'],
  properties: {
    conforme: { type: 'boolean' },
    hallazgos: {
      type: 'array',
      items: {
        type: 'object',
        required: ['titulo', 'severidad', 'cita_espec', 'linea_diff', 'explicacion'],
        properties: {
          titulo: { type: 'string' },
          severidad: { enum: ['alta', 'media', 'baja'] },
          cita_espec: { type: 'string', description: 'Frase TEXTUAL de la espec violada, con su §N' },
          linea_diff: { type: 'string', description: 'fichero:línea del diff' },
          explicacion: { type: 'string' },
        },
      },
    },
    caminos_intentados: { type: 'array', items: { type: 'string' } },
  },
}

const VEREDICTO_VERIFICADOR = {
  type: 'object',
  required: ['veredicto', 'evidencia'],
  properties: {
    veredicto: { enum: ['REAL', 'FALSO_POSITIVO', 'NO_CONCLUYENTE'] },
    evidencia: { type: 'string' },
  },
}

// La plantilla literal de juicio/ — args y nada más.
const cabecera = [
  `Sesión: ${args.sesion}`,
  `Espec que gobierna este diff: ${args.espec}`,
  '',
  'Diff a juzgar (verbatim):',
  args.diff,
].join('\n')

const JUECES = [
  { lente: 'espec', agentType: 'juez-espec', extra: '' },
  {
    lente: 'tests',
    agentType: 'juez-tests',
    extra: args.mutantes ? `\n\nInforme de mutantes LIVED (evidencia mecánica):\n${args.mutantes}` : '',
  },
  { lente: 'concurrencia', agentType: 'juez-concurrencia', extra: '' },
]

phase('Juicio')
log('Lanzando panel completo: espec, tests, concurrencia')

// pipeline: cada lente pasa a verificación en cuanto su juez termina, sin
// esperar a las demás.
const porLente = await pipeline(
  JUECES,
  (j) =>
    agent(`${cabecera}${j.extra}\n\nEmite tu veredicto.`, {
      label: `juez:${j.lente}`,
      phase: 'Juicio',
      agentType: j.agentType,
      schema: VEREDICTO_JUEZ,
    }),
  (veredicto, j) => {
    if (!veredicto) return null
    if (!veredicto.hallazgos.length) return { lente: j.lente, veredicto, verificados: [] }
    // Un verificador fresco por hallazgo: recibe SOLO el hallazgo + el diff,
    // jamás el razonamiento del juez ni los otros hallazgos.
    return parallel(
      veredicto.hallazgos.map((h) => () =>
        agent(
          [
            'Hallazgo a verificar (de un revisor cuyo razonamiento no conoces):',
            `- Título: ${h.titulo}`,
            `- Espec supuestamente violada: «${h.cita_espec}»`,
            `- Línea del diff: ${h.linea_diff}`,
            `- Explicación del hallazgo: ${h.explicacion}`,
            '',
            'Diff en cuestión (verbatim):',
            args.diff,
            '',
            'Tu mandato: demuestra que este hallazgo es FALSO. Emite tu veredicto.',
          ].join('\n'),
          {
            label: `verif:${h.titulo.slice(0, 30)}`,
            phase: 'Verificación',
            agentType: 'verificador',
            schema: VEREDICTO_VERIFICADOR,
          },
        ).then((v) => ({ ...h, lente: j.lente, verificacion: v })),
      ),
    ).then((verificados) => ({ lente: j.lente, veredicto, verificados: verificados.filter(Boolean) }))
  },
)

const lentes = porLente.filter(Boolean)
const todos = lentes.flatMap((l) => l.verificados)
const orden = { alta: 0, media: 1, baja: 2 }
const reales = todos
  .filter((h) => h.verificacion?.veredicto === 'REAL')
  .sort((a, b) => orden[a.severidad] - orden[b.severidad])
const dudosos = todos.filter((h) => h.verificacion?.veredicto === 'NO_CONCLUYENTE')
const falsos = todos.filter((h) => h.verificacion?.veredicto === 'FALSO_POSITIVO')

log(`Panel cerrado: ${reales.length} reales, ${dudosos.length} no concluyentes, ${falsos.length} falsos positivos`)

return {
  conforme: reales.length === 0 && dudosos.length === 0,
  hallazgos_reales: reales,
  no_concluyentes: dudosos, // decidir en el hilo principal; no enterrar
  falsos_positivos: falsos, // descartados, con la evidencia del verificador
  caminos_intentados: Object.fromEntries(lentes.map((l) => [l.lente, l.veredicto.caminos_intentados])),
}
