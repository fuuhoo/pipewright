/**
 * Tarjeta «Caché local» (editor de pipelines → pestaña Variables y caché).
 * Nota de redacción: vaciar es seguro; el único coste es una compilación más lenta. No lo plantee como algo destructivo.
 */
export default {
  title: 'Caché local',
  sub: 'Lo que este proyecto deja en el disco del host de compilación; tras vaciar, la siguiente compilación vuelve a descargar',
  kindRepo: 'Espejo del repositorio',
  kindDeps: 'Caché de dependencias',
  reload: 'Volver a medir',
  reloadAria: 'Volver a medir el tamaño de la caché',
  loading: 'Midiendo…',
  emptyNode: 'Todavía no hay caché de este proyecto en esta máquina.',
  total: '{size} en total',
  clearBtn: 'Vaciar caché',
  clearing: 'Vaciando…',
  clearTitle: 'Elimina ambas cachés para que la siguiente compilación empiece limpia',
  clearDisabledTitle: 'Ahora mismo no hay nada ocupando disco; no hace falta vaciar',
  confirmTitle: '¿Vaciar la caché local de este proyecto?',
  confirmLead: 'Se eliminará en el host de compilación:',
  confirmCost: 'No se rompe nada. La siguiente compilación volverá a clonar el repositorio y a descargar dependencias, así que será más lenta una vez.',
  confirmShared: 'El espejo se guarda por URL de repositorio, así que otros proyectos que lo compartan también se vacían.',
  confirmBtn: 'Vaciar',
  cleared: 'Liberados {size}',
  wasEmpty: 'Ambas cachés ya estaban vacías; no hay nada que borrar.',
  loadFailed: 'No se pudo leer el tamaño de la caché',
  clearFailed: 'Error al vaciar la caché',
}
