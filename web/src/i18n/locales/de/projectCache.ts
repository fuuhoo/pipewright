/**
 * Karte „Lokaler Cache“ (Pipeline-Editor → Tab „Variablen und Cache“).
 * Formulierungen: Leeren ist unkritisch — der einzige Preis ist ein langsamerer Build. Nicht als destruktiv darstellen.
 */
export default {
  title: 'Lokaler Cache',
  sub: 'Was dieses Projekt auf der Disk des Build-Hosts hinterlässt; nach dem Leeren holt der nächste Build alles neu',
  kindRepo: 'Repo-Spiegel',
  kindDeps: 'Abhängigkeits-Cache',
  reload: 'Größe neu messen',
  reloadAria: 'Cache-Größe neu messen',
  loading: 'Vermessung läuft…',
  emptyNode: 'Auf diesem Rechner gibt es noch keinen Cache für dieses Projekt.',
  total: '{size} gesamt',
  clearBtn: 'Cache leeren',
  clearing: 'Wird geleert…',
  clearTitle: 'Beide Caches löschen, damit der nächste Build bei Null startet',
  clearDisabledTitle: 'Momentan belegt nichts den Speicher — Leeren ist nicht nötig',
  confirmTitle: 'Lokalen Cache dieses Projekts leeren?',
  confirmLead: 'Auf dem Build-Host wird gelöscht:',
  confirmCost: 'Nichts geht kaputt. Der nächste Build klont das Repo und lädt Abhängigkeiten erneut herunter — er ist einmal langsamer.',
  confirmShared: 'Der Repo-Spiegel ist nach Repo-URL abgelegt; andere Projekte mit demselben Repo sind davon ebenfalls betroffen.',
  confirmBtn: 'Leeren',
  cleared: '{size} freigegeben',
  wasEmpty: 'Beide Caches waren bereits leer — es gab nichts zu löschen.',
  loadFailed: 'Cache-Größe konnte nicht gelesen werden',
  clearFailed: 'Cache konnte nicht geleert werden',
}
