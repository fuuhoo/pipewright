/**
 * "Local cache" card (pipeline editor → Variables & cache tab).
 * Wording note: clearing is safe — the only cost is one slower build. Don't phrase it as destructive.
 */
export default {
  title: 'Local cache',
  sub: 'What this project leaves on the build host’s disk; the next build re-fetches after a clear',
  kindRepo: 'Repo mirror',
  kindDeps: 'Dependency cache',
  reload: 'Re-measure',
  reloadAria: 'Re-measure cache size',
  loading: 'Measuring…',
  emptyNode: 'No cache for this project on this machine yet.',
  total: '{size} total',
  clearBtn: 'Clear cache',
  clearing: 'Clearing…',
  clearTitle: 'Delete both caches so the next build starts from a clean state',
  clearDisabledTitle: 'Nothing is taking up disk right now — no need to clear',
  confirmTitle: 'Clear this project’s local cache?',
  confirmLead: 'This deletes, on the build host:',
  confirmCost: 'Nothing breaks. The next build just re-clones the repository and re-downloads dependencies, so it runs slower once.',
  confirmShared: 'The repo mirror is keyed by repository URL, so other projects sharing this repository are cleared too.',
  confirmBtn: 'Clear',
  cleared: 'Freed {size}',
  wasEmpty: 'Both caches were already empty — nothing to clear.',
  loadFailed: 'Could not read cache sizes',
  clearFailed: 'Failed to clear cache',
}
