/**
 * Carte «Cache locale » (éditeur de pipeline → onglet Variables et cache).
 * Point de rédaction : vider est sans danger, le seul coût est un build plus lent. Ne pas formuler comme une action destructrice.
 */
export default {
  title: 'Cache locale',
  sub: 'Ce que ce projet laisse sur le disque de l’hôte de build ; après vidage, le build suivant retélécharge',
  kindRepo: 'Miroir du dépôt',
  kindDeps: 'Cache de dépendances',
  reload: 'Remesurer',
  reloadAria: 'Remesurer la taille du cache',
  loading: 'Mesure en cours…',
  emptyNode: 'Aucun cache pour ce projet sur cette machine pour l’instant.',
  total: '{size} au total',
  clearBtn: 'Vider le cache',
  clearing: 'Vidage…',
  clearTitle: 'Supprimer les deux caches pour repartir d’un état propre au prochain build',
  clearDisabledTitle: 'Rien n’occupe le disque actuellement, rien à vider',
  confirmTitle: 'Vider le cache local de ce projet ?',
  confirmLead: 'Cela supprimera, sur l’hôte de build :',
  confirmCost: 'Rien ne se casse. Le prochain build re-clonera le dépôt et re-téléchargera les dépendances ; il sera simplement plus lent une fois.',
  confirmShared: 'Le miroir est stocké par URL de dépôt ; les autres projets qui partagent ce dépôt sont vidés eux aussi.',
  confirmBtn: 'Vider',
  cleared: '{size} libérés',
  wasEmpty: 'Les deux caches étaient déjà vides — rien à supprimer.',
  loadFailed: 'Lecture de la taille du cache impossible',
  clearFailed: 'Échec du vidage du cache',
}
