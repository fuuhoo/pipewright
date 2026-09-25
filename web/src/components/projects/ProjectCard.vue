<script setup lang="ts">
/**
 * 项目卡片 —— 项目页「卡片视图」与「分组视图」共用同一张卡。
 *
 * 抽出来之前这段模板内联在 Projects.vue 里:两种视图各写一遍的话,卡片上那七个动作
 * 与权限态(能不能改归属)迟早会走样,而这两处正是出错最疼的地方。
 * 视图只负责挑项目、排布局;怎么展示一张卡、点按钮算哪个动作,都留在这里。
 */
import { useI18n } from 'vue-i18n'
import type { Project, RunStatus } from '../../api/projects'
import { runStatusLabel } from '../../lib/runStatus'

const props = defineProps<{
  project: Project
  /** 归属分组的展示名(未归组 / 组已删除都由调用方算好)。 */
  groupLabel: string
  /** 当前用户能否改这张卡的归属 —— 与后端 Manage 判定同构,组件不自行推权限。 */
  canAssignGroup: boolean
}>()

const emit = defineEmits<{
  run: [project: Project]
  rename: [project: Project]
  repo: [project: Project]
  code: [project: Project]
  pipeline: [project: Project]
  remove: [project: Project]
  assign: [project: Project]
}>()

const { t } = useI18n()

// Status pill config — fixed six-word vocabulary, no substitutes
type StatusConfig = { dot: string; bg: string; border: string; text: string; pulse: boolean }

const STATUS_CONFIG: Record<RunStatus, StatusConfig> = {
  '成功':   { dot: 'var(--color-green)',  bg: 'var(--color-green-soft)',  border: 'transparent',            text: 'var(--color-green)',  pulse: false },
  '失败':   { dot: 'var(--color-red)',    bg: 'var(--color-red-soft)',    border: 'var(--color-red-line)',   text: 'var(--color-red)',    pulse: false },
  '进行中': { dot: 'var(--color-amber)',  bg: 'var(--color-amber-soft)',  border: 'transparent',            text: 'var(--color-amber)',  pulse: true  },
  '部分失败': { dot: 'var(--color-red)',  bg: 'var(--color-red-soft)',    border: 'var(--color-red-line)',   text: 'var(--color-red)',    pulse: false },
  '已回滚': { dot: 'var(--color-amber)',  bg: 'var(--color-amber-soft)',  border: 'var(--color-amber-line)', text: 'var(--color-amber)',  pulse: false },
  '排队中': { dot: 'var(--color-faint)',  bg: 'var(--color-card-2)',      border: 'var(--color-border-strong)', text: 'var(--color-dim)', pulse: false },
}

function relativeTime(isoStr: string): string {
  const diff = Date.now() - new Date(isoStr).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return t('time.justNow')
  const m = Math.floor(s / 60)
  if (m < 60) return t('time.minAgo', { n: m })
  const h = Math.floor(m / 60)
  if (h < 24) return t('time.hourAgo', { n: h })
  const d = Math.floor(h / 24)
  return t('time.dayAgo', { n: d })
}
</script>

<template>
  <li class="project-card">
    <!-- Card header: name + status badge -->
    <div class="card-header">
      <div class="project-icon" aria-hidden="true">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
          <path d="M14.5 9.5 21 3M21 3h-5M21 3v5"/>
          <path d="M10 14a5 5 0 1 1-7 4.6"/>
        </svg>
      </div>
      <h2 class="project-name" :title="project.name">{{ project.name }}</h2>

      <!-- Status badge: only if we have a run status -->
      <div
        v-if="project.lastRunStatus"
        class="status-pill"
        :style="{
          background: STATUS_CONFIG[project.lastRunStatus].bg,
          border: `1px solid ${STATUS_CONFIG[project.lastRunStatus].border}`,
          color: STATUS_CONFIG[project.lastRunStatus].text,
        }"
        :aria-label="t('projects.runStatusAria', { status: runStatusLabel(project.lastRunStatus) })"
      >
        <span
          class="status-dot"
          :class="{ 'status-dot--pulse': STATUS_CONFIG[project.lastRunStatus].pulse }"
          :style="{ background: STATUS_CONFIG[project.lastRunStatus].dot }"
          aria-hidden="true"
        />
        {{ runStatusLabel(project.lastRunStatus) }}
      </div>
    </div>

    <!-- Repo + branch (equal-width columns);纯发布项目没有这两行,直接说明用途 -->
    <div class="card-repo">
      <div class="repo-url-row">
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M9 19c-5 1.5-5-2.5-7-3m14 6v-3.87a3.37 3.37 0 0 0-.94-2.61c3.14-.35 6.44-1.54 6.44-7A5.44 5.44 0 0 0 20 4.77 5.07 5.07 0 0 0 19.91 1S18.73.65 16 2.48a13.38 13.38 0 0 0-7 0C6.27.65 5.09 1 5.09 1A5.07 5.07 0 0 0 5 4.77a5.44 5.44 0 0 0-1.5 3.78c0 5.42 3.3 6.61 6.44 7A3.37 3.37 0 0 0 9 18.13V22"/>
        </svg>
        <a
          v-if="project.repoUrl"
          class="repo-url mono"
          :href="project.repoUrl"
          target="_blank"
          rel="noopener noreferrer"
          :title="project.repoUrl"
        >{{ project.repoUrl.replace(/^https?:\/\//, '') }}</a>
        <span v-else class="repo-url repo-url--unbound mono">{{ t('projects.repoNotBound') }}</span>
      </div>
      <div v-if="project.repoUrl" class="branch-row">
        <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
          <path d="M6 3v12"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>
        </svg>
        <span class="branch-name mono">{{ project.defaultBranch || '—' }}</span>
      </div>
    </div>

    <!-- Divider -->
    <div class="card-divider" aria-hidden="true" />

    <!-- Last run: empty placeholder or status -->
    <div class="card-meta-row">
      <span class="meta-label">{{ t('projects.lastRun') }}</span>
      <span
        v-if="!project.lastRunStatus"
        class="meta-empty"
      >{{ t('projects.noRun') }}</span>
      <span
        v-else
        class="meta-value"
        :style="{ color: STATUS_CONFIG[project.lastRunStatus].text }"
      >{{ runStatusLabel(project.lastRunStatus) }}</span>
    </div>

    <!-- Target servers: empty placeholder or list -->
    <div class="card-meta-row">
      <span class="meta-label">{{ t('projects.targetServers') }}</span>
      <span
        v-if="!project.targetServers || project.targetServers.length === 0"
        class="meta-empty"
      >{{ t('projects.notBound') }}</span>
      <span v-else class="meta-value">
        {{ project.targetServers.join(', ') }}
      </span>
    </div>

    <!-- Credential reference: display name + masked, never plaintext -->
    <div class="card-meta-row">
      <span class="meta-label">{{ t('projects.credential') }}</span>
      <span class="meta-value meta-value--mono" :title="t('projects.credentialRefTitle')">
        {{ project.credentialName || '—' }}
      </span>
    </div>

    <!-- 分组:决定谁能看/能操作;有归属管理权时可直接点组名改组 -->
    <div class="card-meta-row">
      <span class="meta-label">{{ t('groups.fieldGroup') }}</span>
      <button
        v-if="canAssignGroup"
        class="meta-value group-link"
        :title="t('groups.assignAction', { name: project.name })"
        @click="emit('assign', project)"
      >
        {{ groupLabel }}
      </button>
      <span
        v-else
        class="meta-value"
        :title="t('groups.lockedHint')"
      >{{ groupLabel }}</span>
    </div>

    <!-- Card footer: updatedAt + actions -->
    <div class="card-footer">
      <span class="card-time" :title="project.updatedAt">
        {{ t('projects.updatedAt', { time: relativeTime(project.updatedAt) }) }}
      </span>

      <div class="card-actions">
        <!-- Manual trigger / Run -->
        <button
          class="action-btn action-btn--run"
          :title="t('projects.actionRunTitle', { name: project.name })"
          :aria-label="t('projects.actionRunAria', { name: project.name })"
          @click.stop="emit('run', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <polygon points="5 3 19 12 5 21 5 3" fill="currentColor" stroke="none"/>
          </svg>
        </button>

        <!-- Rename -->
        <button
          class="action-btn"
          :title="t('projects.actionRenameTitle', { name: project.name })"
          :aria-label="t('projects.actionRenameAria', { name: project.name })"
          @click="emit('rename', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
            <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
            <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
          </svg>
        </button>

        <!-- 仓库设置:绑定 / 改绑 / 解绑(解绑即退回「只发布」) -->
        <button
          class="action-btn"
          :title="t('projects.actionRepoTitle', { name: project.name })"
          :aria-label="t('projects.actionRepoAria', { name: project.name })"
          @click="emit('repo', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.72"/>
            <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
          </svg>
        </button>

        <!-- Code browse (Story 7-4: read-only source viewer, FR-4) — 没仓库就无从浏览 -->
        <button
          v-if="project.repoUrl"
          class="action-btn"
          :title="t('projects.actionCodeTitle', { name: project.name })"
          :aria-label="t('projects.actionCodeAria', { name: project.name })"
          @click="emit('code', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <polyline points="16 18 22 12 16 6"/>
            <polyline points="8 6 2 12 8 18"/>
          </svg>
        </button>

        <!-- Configure → triggers page (Story 2.3; will be extended to 4-tab editor in 2-2) -->
        <button
          class="action-btn"
          :title="t('projects.actionPipelineTitle', { name: project.name })"
          :aria-label="t('projects.actionPipelineAria', { name: project.name })"
          @click="emit('pipeline', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
            <circle cx="12" cy="12" r="3"/>
            <path d="M19.07 4.93a10 10 0 1 1-14.14 0"/>
            <path d="M12 2v4M12 18v4M4.93 4.93 7.76 7.76M16.24 16.24l2.83 2.83"/>
          </svg>
        </button>

        <!-- Delete -->
        <button
          class="action-btn action-btn--danger"
          :title="t('projects.actionDeleteTitle', { name: project.name })"
          :aria-label="t('projects.actionDeleteAria', { name: project.name })"
          @click="emit('remove', project)"
        >
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9">
            <path d="M3 6h18M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
            <path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/>
          </svg>
        </button>
      </div>
    </div>
  </li>
</template>

<style scoped>
/* ─── project card ──────────────────────────────────────────────────────────── */
.project-card {
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--rounded-card);
  box-shadow: var(--shadow);
  padding: 18px 20px 16px;
  display: flex;
  flex-direction: column;
  gap: 0;
  animation: card-in 0.4s var(--ease-out-expo) both;
  transition: border-color var(--duration-fast), box-shadow var(--duration-fast), transform var(--duration-fast);
}

.project-card:hover {
  border-color: var(--color-border-strong);
  transform: translateY(-2px);
  box-shadow: var(--shadow), 0 0 0 1px var(--color-border-strong);
}

@keyframes card-in {
  from { opacity: 0; transform: translateY(13px); }
  to   { opacity: 1; transform: none; }
}

@media (prefers-reduced-motion: reduce) {
  .project-card {
    animation: none;
  }
  .project-card:hover {
    transform: none;
  }
}

/* card header: icon + name + status badge */
.card-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}

.project-icon {
  width: 30px;
  height: 30px;
  border-radius: var(--rounded);
  background: var(--color-primary-soft);
  color: var(--color-primary);
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.project-name {
  flex: 1;
  font-size: 0.95rem;
  font-weight: 600;
  color: var(--color-text);
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 1.3;
}

/* status pill */
.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: var(--rounded-md);
  font-size: var(--text-micro);
  font-weight: 600;
  white-space: nowrap;
  flex-shrink: 0;
}

.status-dot {
  width: 6px;
  height: 6px;
  border-radius: var(--rounded-full);
  flex-shrink: 0;
}

.status-dot--pulse {
  animation: dot-pulse 1.1s ease-in-out infinite;
}

@keyframes dot-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%       { opacity: 0.5; transform: scale(0.8); }
}

@media (prefers-reduced-motion: reduce) {
  .status-dot--pulse {
    animation: none;
  }
}

/* repo + branch */
.card-repo {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 14px;
}

.repo-url-row,
.branch-row {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--color-faint);
}

.repo-url {
  font-size: 0.74rem;
  color: var(--color-dim);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  text-decoration: none;
  flex: 1;
  min-width: 0;
}

.repo-url:hover {
  color: var(--color-primary);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.repo-url--unbound {
  color: var(--color-faint);
}

.repo-url:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
  border-radius: 2px;
}

.branch-name {
  font-size: 0.72rem;
  color: var(--color-dim);
}

.mono {
  font-family: var(--font-mono);
}

/* divider */
.card-divider {
  height: 1px;
  background: var(--color-border);
  margin-bottom: 12px;
}

/* meta rows */
.card-meta-row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 7px;
  min-height: 20px;
}

.card-meta-row:last-of-type {
  margin-bottom: 14px;
}

.meta-label {
  font-size: 0.71rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-faint);
  flex-shrink: 0;
}

.meta-empty {
  font-size: 0.78rem;
  color: var(--color-faint);
  font-style: italic;
}

.meta-value {
  font-size: 0.78rem;
  color: var(--color-dim);
  text-align: right;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 60%;
}

.meta-value--mono {
  font-family: var(--font-mono);
  font-size: 0.72rem;
  letter-spacing: 0.02em;
  user-select: none;
}

/* 分组值可点:只有对该资源归属有管理权时才渲染成按钮(见 canAssignGroup)。 */
.group-link {
  appearance: none;
  background: none;
  border: none;
  padding: 0;
  margin: 0 0 0 auto;
  font: inherit;
  color: var(--color-primary);
  text-decoration: underline dotted;
  text-underline-offset: 2px;
  cursor: pointer;
}

/* card footer */
.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: auto;
  padding-top: 10px;
  border-top: 1px solid var(--color-border);
}

.card-time {
  font-size: 0.72rem;
  color: var(--color-faint);
}

.card-actions {
  display: flex;
  gap: 5px;
}

.action-btn {
  width: 28px;
  height: 26px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-faint);
  border-radius: var(--rounded-md);
  cursor: pointer;
  display: grid;
  place-items: center;
  transition:
    color var(--duration-fast),
    border-color var(--duration-fast),
    background-color var(--duration-fast);
}

.action-btn:hover {
  color: var(--color-text);
  border-color: var(--color-faint);
}

.action-btn:focus-visible {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
}

.action-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

.action-btn--danger:hover {
  color: var(--color-red);
  border-color: var(--color-red-line);
  background: var(--color-red-soft);
}

.action-btn--run:hover {
  color: var(--color-primary);
  border-color: var(--color-primary);
  background: var(--color-primary-soft);
}
</style>
