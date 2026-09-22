<script setup lang="ts">
/**
 * v6.2 配置资源管理(admin-only)。
 *
 * 覆盖 §3.3:
 *   - CRUD;内置行(is_builtin)仅可改 description/enabled → 403 builtin_readonly
 *   - multipart 上传(扩展名白名单 + 大小上限,后端 PIPEWRIGHT_CONFIG_UPLOAD_MAX_SIZE)
 *   - 磁盘权威副本在 DATA_DIR/config_profiles/<id>/,DB content 为冗余快照
 */
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  listConfigProfiles,
  createConfigProfile,
  updateConfigProfile,
  deleteConfigProfile,
  uploadConfigProfile,
  isExtAllowed,
  CONFIG_UPLOAD_EXTS,
} from '../../api/configProfiles'
import type { ConfigProfile, ConfigProfileInput } from '../../api/configProfiles'
import { HttpError } from '../../api/http'

const { t } = useI18n()

const extsText = CONFIG_UPLOAD_EXTS.join(' / ')
const maxText = '1 MB'

// ─── state ──────────────────────────────────────────────────────────────────

const loadState = ref<'idle' | 'loading' | 'error'>('idle')
const loadError = ref('')
const profiles = ref<ConfigProfile[]>([])

// ─── form modal ─────────────────────────────────────────────────────────────

const modalOpen = ref(false)
const editingId = ref<string | null>(null)
const formSubmitting = ref(false)
const formBanner = ref('')

const emptyForm = (): ConfigProfileInput => ({
  language: '',
  configType: '',
  name: '',
  targetPath: '',
  content: '',
  isDefault: false,
  description: '',
  enabled: true,
})

const form = ref<ConfigProfileInput>(emptyForm())
const editingBuiltin = ref(false)

function openAdd(): void {
  editingId.value = null
  editingBuiltin.value = false
  form.value = emptyForm()
  formFile.value = null
  formBanner.value = ''
  modalOpen.value = true
}

function openEdit(p: ConfigProfile): void {
  editingId.value = p.id
  editingBuiltin.value = p.isBuiltin
  // 内置行回传全部原值,只放开 description/enabled(后端同样会校验白名单)。
  form.value = {
    language: p.language,
    configType: p.configType,
    name: p.name,
    targetPath: p.targetPath,
    content: '',
    isDefault: p.isDefault,
    description: p.description,
    enabled: p.enabled,
  }
  formFile.value = null
  formBanner.value = ''
  modalOpen.value = true
}

// ─── delete ─────────────────────────────────────────────────────────────────

const deleteOpen = ref(false)
const deleting = ref<ConfigProfile | null>(null)
const deleteSubmitting = ref(false)
const deleteBanner = ref('')

// ─── upload (并入新建弹窗:选了文件就走 multipart 上传,忽略 content 文本框) ───

const formFile = ref<File | null>(null)
const acceptExts = CONFIG_UPLOAD_EXTS.join(',')

function onFormFilePick(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  if (file && !isExtAllowed(file.name)) {
    formBanner.value = t('configProfiles.errExt', { exts: extsText })
    formFile.value = null
    input.value = ''
    return
  }
  formFile.value = file
}

// ─── data ───────────────────────────────────────────────────────────────────

async function load(): Promise<void> {
  loadState.value = 'loading'
  loadError.value = ''
  try {
    profiles.value = await listConfigProfiles()
    loadState.value = 'idle'
  } catch (err) {
    loadError.value = msg(err, 'configProfiles.errLoad')
    loadState.value = 'error'
  }
}

onMounted(load)

function msg(err: unknown, fallback: string): string {
  if (err instanceof HttpError) {
    if (err.status === 0) return t('configProfiles.errLoadConn')
    return err.apiError?.message ?? t(fallback)
  }
  return t(fallback)
}

// ─── actions ────────────────────────────────────────────────────────────────

async function submitForm(): Promise<void> {
  formSubmitting.value = true
  formBanner.value = ''
  try {
    if (editingId.value) {
      await updateConfigProfile(editingId.value, form.value)
    } else if (formFile.value) {
      await uploadConfigProfile({
        file: formFile.value,
        language: form.value.language,
        configType: form.value.configType,
        name: form.value.name,
        targetPath: form.value.targetPath,
        description: form.value.description,
        isDefault: form.value.isDefault,
      })
    } else {
      await createConfigProfile(form.value)
    }
    modalOpen.value = false
    await load()
  } catch (err) {
    formBanner.value = msg(err, 'configProfiles.errSave')
  } finally {
    formSubmitting.value = false
  }
}

async function confirmDelete(): Promise<void> {
  if (!deleting.value) return
  deleteSubmitting.value = true
  deleteBanner.value = ''
  try {
    await deleteConfigProfile(deleting.value.id)
    deleteOpen.value = false
    await load()
  } catch (err) {
    deleteBanner.value = msg(err, 'configProfiles.errDelete')
  } finally {
    deleteSubmitting.value = false
  }
}
</script>

<template>
  <div class="cp-view">
    <header class="view-header">
      <div>
        <h1 class="view-title">{{ t('configProfiles.title') }}</h1>
        <p class="view-sub">{{ t('configProfiles.desc') }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn--primary" @click="openAdd">
          + {{ t('configProfiles.add') }}
        </button>
      </div>
    </header>

    <p v-if="loadState === 'loading'" class="state">{{ t('common.refresh') }}…</p>
    <div v-else-if="loadState === 'error'" class="state state--error">
      <p>{{ loadError }}</p>
      <button class="btn" @click="load">{{ t('configProfiles.save') }}</button>
    </div>
    <p v-else-if="profiles.length === 0" class="state">{{ t('configProfiles.empty') }}</p>

    <table v-else class="grid">
      <thead>
        <tr>
          <th>{{ t('configProfiles.colProfile') }}</th>
          <th>{{ t('configProfiles.colTarget') }}</th>
          <th>{{ t('configProfiles.colBuiltin') }}</th>
          <th>{{ t('configProfiles.colEnabled') }}</th>
          <th>{{ t('configProfiles.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="p in profiles" :key="p.id">
          <td>
            <div class="cell-strong">
              {{ p.name }}
              <span v-if="p.isDefault" class="tag">{{ t('configProfiles.default') }}</span>
            </div>
            <div class="cell-dim">{{ p.language }} · {{ p.configType }}</div>
          </td>
          <td>
            <code class="mono">{{ p.targetPath }}</code>
            <div class="cell-dim mono mono--sm">{{ p.filePath }}</div>
          </td>
          <td>
            <span class="tag" :class="p.isBuiltin ? 'tag--builtin' : 'tag--custom'">
              {{ p.isBuiltin ? t('configProfiles.builtin') : t('configProfiles.custom') }}
            </span>
            <div v-if="p.isBuiltin" class="cell-dim">{{ t('configProfiles.builtinHint') }}</div>
          </td>
          <td>{{ p.enabled ? t('configProfiles.colEnabled') : '—' }}</td>
          <td class="actions">
            <button class="btn btn--sm" @click="openEdit(p)">{{ t('configProfiles.editAction') }}</button>
            <button
              class="btn btn--sm btn--danger"
              :disabled="p.isBuiltin"
              :title="p.isBuiltin ? t('configProfiles.builtinHint') : ''"
              @click="(deleting = p), (deleteOpen = true), (deleteBanner = '')"
            >
              {{ t('configProfiles.delete') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <!-- ── add / edit ── -->
    <div v-if="modalOpen" class="modal-mask" @click.self="modalOpen = false">
      <div class="modal" role="dialog">
        <h2 class="modal-title">
          {{ editingId ? t('configProfiles.edit') : t('configProfiles.create') }}
        </h2>

        <div class="form-grid">
          <label class="field">
            <span>{{ t('configProfiles.fieldLanguage') }}</span>
            <input v-model="form.language" type="text" :disabled="editingBuiltin" placeholder="java" />
          </label>
          <label class="field">
            <span>{{ t('configProfiles.fieldConfigType') }}</span>
            <input
              v-model="form.configType"
              type="text"
              :disabled="editingBuiltin"
              placeholder="maven-settings"
            />
          </label>
          <label class="field">
            <span>{{ t('configProfiles.fieldName') }}</span>
            <input v-model="form.name" type="text" :disabled="editingBuiltin" />
          </label>
          <label class="field">
            <span>{{ t('configProfiles.fieldIsDefault') }}</span>
            <select v-model="form.isDefault" :disabled="editingBuiltin">
              <option :value="false">—</option>
              <option :value="true">{{ t('configProfiles.default') }}</option>
            </select>
          </label>
          <label class="field field--wide">
            <span>{{ t('configProfiles.fieldTargetPath') }}</span>
            <input
              v-model="form.targetPath"
              type="text"
              :disabled="editingBuiltin"
              :placeholder="t('configProfiles.targetPathPlaceholder')"
            />
            <small>{{ t('configProfiles.targetPathHint') }}</small>
          </label>
          <label v-if="!editingBuiltin" class="field field--wide">
            <span>{{ t('configProfiles.fieldContent') }}</span>
            <input
              v-if="!editingId"
              type="file"
              :accept="acceptExts"
              @change="onFormFilePick"
            />
            <small v-if="!editingId">
              {{
                formFile
                  ? t('configProfiles.uploadOk') + ': ' + formFile.name
                  : t('configProfiles.uploadHint', { exts: extsText, max: maxText })
              }}
            </small>
            <textarea
              v-if="!formFile"
              v-model="form.content"
              rows="10"
              class="mono"
              :disabled="!!formFile"
            />
            <small v-if="!formFile">{{ t('configProfiles.contentHint') }}</small>
          </label>
          <label class="field field--wide">
            <span>{{ t('configProfiles.fieldDescription') }}</span>
            <input v-model="form.description" type="text" />
          </label>
          <label class="field field--wide field--inline">
            <span>{{ t('configProfiles.fieldEnabled') }}</span>
            <input v-model="form.enabled" type="checkbox" />
          </label>
        </div>

        <p v-if="formBanner" class="banner banner--err">{{ formBanner }}</p>

        <footer class="modal-actions">
          <button class="btn" @click="modalOpen = false">{{ t('configProfiles.cancel') }}</button>
          <button class="btn btn--primary" :disabled="formSubmitting" @click="submitForm">
            {{ formSubmitting ? t('configProfiles.saving') : t('configProfiles.save') }}
          </button>
        </footer>
      </div>
    </div>

    <!-- ── delete ── -->
    <div v-if="deleteOpen" class="modal-mask" @click.self="deleteOpen = false">
      <div class="modal modal--sm" role="dialog">
        <h2 class="modal-title">{{ t('configProfiles.confirmDelete') }}</h2>
        <p class="modal-body"><strong>{{ deleting?.name }}</strong></p>
        <p v-if="deleteBanner" class="banner banner--err">{{ deleteBanner }}</p>
        <footer class="modal-actions">
          <button class="btn" @click="deleteOpen = false">{{ t('configProfiles.cancel') }}</button>
          <button class="btn btn--danger" :disabled="deleteSubmitting" @click="confirmDelete">
            {{ t('configProfiles.delete') }}
          </button>
        </footer>
      </div>
    </div>
  </div>
</template>

<style scoped>
.view-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.view-title {
  font-size: var(--text-display);
  font-weight: 700;
  color: var(--color-text);
}
.view-sub {
  font-size: var(--text-body);
  color: var(--color-faint);
  margin-top: 4px;
  max-width: 76ch;
}
.header-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
.state {
  padding: 32px;
  text-align: center;
  color: var(--color-faint);
}
.state--error {
  color: var(--color-danger, #dc2626);
}
.grid {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--text-label);
}
.grid th {
  text-align: left;
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-faint);
  font-weight: 600;
  white-space: nowrap;
}
.grid td {
  padding: 12px;
  border-bottom: 1px solid var(--color-border);
  vertical-align: top;
}
.cell-strong {
  font-weight: 600;
  color: var(--color-text);
}
.cell-dim {
  font-size: var(--text-small, 0.85em);
  color: var(--color-faint);
}
.mono {
  font-family: var(--font-mono, monospace);
  font-size: var(--text-small, 0.85em);
  word-break: break-all;
}
.mono--sm {
  opacity: 0.75;
}
.tag {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: var(--text-small, 0.78em);
  font-weight: 600;
}
.tag--builtin {
  background: rgba(59, 130, 246, 0.15);
  color: #2563eb;
}
.tag--custom {
  background: rgba(120, 120, 120, 0.15);
  color: var(--color-dim);
}
.actions {
  display: flex;
  gap: 6px;
}
.btn {
  padding: 7px 14px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  cursor: pointer;
}
.btn:hover:not(:disabled) {
  border-color: var(--color-primary);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn--primary {
  background: var(--color-primary);
  border-color: var(--color-primary);
  color: #fff;
}
.btn--danger {
  color: var(--color-danger, #dc2626);
  border-color: var(--color-danger, #dc2626);
}
.btn--sm {
  padding: 4px 10px;
  font-size: var(--text-small, 0.85em);
}
.banner {
  padding: 10px 14px;
  border-radius: 8px;
  background: rgba(0, 0, 0, 0.04);
  font-size: var(--text-label);
}
.banner--err {
  background: rgba(220, 38, 38, 0.1);
  color: #dc2626;
}
.modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.modal {
  background: var(--color-bg, #fff);
  border-radius: 12px;
  padding: 24px;
  width: min(680px, 92vw);
  max-height: 88vh;
  overflow: auto;
}
.modal--sm {
  width: min(420px, 92vw);
}
.modal-title {
  font-size: var(--text-h3, 1.1rem);
  font-weight: 700;
  margin-bottom: 8px;
}
.modal-sub {
  font-size: var(--text-label);
  color: var(--color-faint);
  margin-bottom: 16px;
}
.modal-body {
  color: var(--color-dim);
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  font-size: var(--text-label);
}
.field--wide {
  grid-column: 1 / -1;
}
.field--inline {
  flex-direction: row;
  align-items: center;
  gap: 8px;
}
.field > span {
  font-weight: 600;
  color: var(--color-dim);
}
.field input,
.field select,
.field textarea {
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-bg, #fff);
  color: var(--color-text);
  font-size: var(--text-label);
  font-family: inherit;
}
.field textarea {
  resize: vertical;
}
.field small {
  color: var(--color-faint);
  font-size: var(--text-small, 0.85em);
}
</style>
