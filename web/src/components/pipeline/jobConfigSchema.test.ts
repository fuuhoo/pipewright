import { describe, it, expect } from 'vitest'
import {
  JOB_TYPE_SPECS,
  JOB_TYPE_OPTIONS,
  PICKABLE_TYPES,
  getJobTypeSpec,
  jobTypeLabel,
  jobTypeAccent,
  groupedJobTypes,
  canonicalJobType,
  jobTemplatePrefill,
  isScriptClassType,
  effectiveJobType,
  normalizeArtifactTier,
  usesBuildTierField,
  schemaKeys,
  splitConfig,
  droppedKeys,
  managedKeys,
} from './jobConfigSchema'

describe('jobConfigSchema', () => {
  it('exposes a typed spec for every dropdown option', () => {
    for (const opt of JOB_TYPE_OPTIONS) {
      const spec = getJobTypeSpec(opt.value)
      expect(spec, `spec for ${opt.value}`).not.toBeNull()
      expect(spec!.fields.length).toBeGreaterThan(0)
      expect(opt.label).toContain(spec!.label)
    }
  })

  it('returns null for an unknown type and falls back to the raw token label', () => {
    expect(getJobTypeSpec('totally_unknown')).toBeNull()
    expect(jobTypeLabel('totally_unknown')).toBe('totally_unknown')
    expect(jobTypeLabel('build')).toBe('构建')
    // 旧镜像类型有自己可读的旧版标签(否则画布上两种节点看起来一模一样,分不清该改哪个)。
    expect(jobTypeLabel('build_image')).not.toBe(jobTypeLabel('build'))
  })

  it('every spec has a valid accent and category', () => {
    const accents = ['cyan', 'primary', 'green', 'amber', 'red', 'neutral']
    for (const spec of Object.values(JOB_TYPE_SPECS)) {
      expect(accents, `${spec.type} accent`).toContain(spec.accent)
      expect(typeof spec.category).toBe('string')
    }
  })

  describe('groupedJobTypes', () => {
    it('covers every pickable type exactly once across groups', () => {
      const grouped = groupedJobTypes()
      const flat = grouped.flatMap((g) => g.specs.map((s) => s.type))
      expect(flat.slice().sort()).toEqual([...PICKABLE_TYPES].sort())
      expect(new Set(flat).size).toBe(PICKABLE_TYPES.length)
    })

    it('omits empty groups and keeps display order', () => {
      const grouped = groupedJobTypes()
      for (const g of grouped) expect(g.specs.length).toBeGreaterThan(0)
      // source group comes before custom group
      const ids = grouped.map((g) => g.id)
      expect(ids.indexOf('source')).toBeLessThan(ids.indexOf('custom'))
    })

    it('never lists the custom alias as a pickable type', () => {
      expect(PICKABLE_TYPES).not.toContain('custom')
    })

    // 撤销的类型:picker 与表单都不再出现(存量节点由后端保存校验点名要求改配)。
    it('no longer offers the retired health_check task type', () => {
      expect(PICKABLE_TYPES).not.toContain('health_check')
      expect(getJobTypeSpec('health_check')).toBeNull()
      expect(groupedJobTypes().map((g) => g.id)).not.toContain('quality')
    })
  })

  // 健康门控改成部署任务的一个配置项(执行侧 deploy.HealthCheckFromConfig 读这些键)。
  describe('deploy health probe fields', () => {
    const fields = JOB_TYPE_SPECS.deploy_ssh.fields

    function visible(config: Record<string, string>): string[] {
      return fields.filter((f) => !f.when || f.when(config)).map((f) => f.key)
    }

    it('is off by default and expands per probe mode', () => {
      expect(visible({})).toContain('healthProbe')
      expect(visible({})).not.toContain('healthUrl')
      expect(visible({ healthProbe: 'none' })).not.toContain('healthCommand')
      expect(visible({ healthProbe: 'http' })).toContain('healthUrl')
      expect(visible({ healthProbe: 'http' })).toContain('healthTimeout')
      expect(visible({ healthProbe: 'http' })).not.toContain('healthCommand')
      expect(visible({ healthProbe: 'command' })).toContain('healthCommand')
      expect(visible({ healthProbe: 'command' })).not.toContain('healthUrl')
    })

    it('deploy_frontend legacy node shares the same fields', () => {
      expect(JOB_TYPE_SPECS.deploy_frontend.fields.map((f) => f.key)).toContain('healthProbe')
    })

    // 键名是跨端契约:pipeline.ConfigKeyHealth* / deploy.CfgKeyHealth* 与之逐字相同。
    it('uses the exact key names the backend reads', () => {
      const keys = fields.map((f) => f.key)
      for (const k of ['healthProbe', 'healthUrl', 'healthCommand', 'healthRetries', 'healthInterval', 'healthTimeout']) {
        expect(keys, k).toContain(k)
      }
    })
  })

  // 模板是「预填配方」而不是节点类型:两型合一后,前端部署只作为部署任务的模板存在。
  describe('task templates', () => {
    it('deploy_frontend is not pickable but still renders legacy nodes', () => {
      expect(PICKABLE_TYPES).not.toContain('deploy_frontend')
      expect(getJobTypeSpec('deploy_frontend')).not.toBeNull()
    })

    it('prefills only keys the owning type actually renders', () => {
      for (const spec of Object.values(JOB_TYPE_SPECS)) {
        const keys = schemaKeys(spec.type)
        for (const tpl of spec.templates ?? []) {
          expect(tpl.label, `${spec.type}/${tpl.id} 缺名称`).toBeTruthy()
          for (const k of Object.keys(tpl.prefill)) {
            expect(keys, `${spec.type}/${tpl.id} 预填了表单外的键 ${k}`).toContain(k)
          }
        }
      }
    })

    it('frontend static template prefills dist + rolling + nginx reload', () => {
      expect(jobTemplatePrefill('deploy_ssh', 'frontend_static')).toEqual({
        artifactType: 'dist',
        strategy: 'rolling',
        restartCommand: 'nginx -s reload',
      })
      expect(jobTemplatePrefill('deploy_ssh', 'nope')).toEqual({})
      expect(jobTemplatePrefill('no_such_type', 'frontend_static')).toEqual({})
    })
  })

  // custom 只是 script 的历史别名:不进 picker、表单按 script 渲染,保存时收敛回 script。
  describe('legacy type aliases', () => {
    it('collapses the custom alias onto script', () => {
      expect(PICKABLE_TYPES).not.toContain('custom')
      expect(canonicalJobType('custom')).toBe('script')
      expect(canonicalJobType('script')).toBe('script')
      expect(canonicalJobType('totally_unknown')).toBe('totally_unknown')
    })

    it('renders the alias with exactly its target type form', () => {
      const alias = JOB_TYPE_SPECS.custom
      const target = JOB_TYPE_SPECS.script
      expect(alias.aliasOf).toBe('script')
      // 同一份字段定义(不是复制):别名节点与 script 的表单必然一致。
      expect(alias.fields).toBe(target.fields)
      expect(alias.label).toBe(target.label)
      expect(alias.managedKeys).toEqual(target.managedKeys)
    })
  })

  it('jobTypeAccent falls back to neutral for unknown types', () => {
    expect(jobTypeAccent('totally_unknown')).toBe('neutral')
  })

  it('every field key is unique within a type', () => {
    for (const spec of Object.values(JOB_TYPE_SPECS)) {
      const keys = spec.fields.map((f) => f.key)
      expect(new Set(keys).size, `duplicate key in ${spec.type}`).toBe(keys.length)
    }
  })

  it('select/credential fields are well-formed', () => {
    for (const spec of Object.values(JOB_TYPE_SPECS)) {
      for (const f of spec.fields) {
        if (f.kind === 'select') {
          expect(f.options && f.options.length > 0, `${spec.type}.${f.key} needs options`).toBe(
            true,
          )
        }
        if (f.kind === 'credential') {
          expect(typeof f.credentialType === 'string' || f.credentialType === undefined).toBe(true)
        }
      }
    }
  })

  // 构建任务合并:一张卡,产物档位决定表单展开哪一套字段(与后端 EffectiveJobType 同一口径)。
  describe('build task artifact tiers', () => {
    const fields = JOB_TYPE_SPECS.build.fields

    function visible(config: Record<string, string>): string[] {
      return fields.filter((f) => !f.when || f.when(config)).map((f) => f.key)
    }

    it('is the only pickable build task; the old ones stay renderable as legacy specs', () => {
      expect(PICKABLE_TYPES).toContain('build')
      // build_image / build_frontend / build_backend 收进「构建」;push_image 的真实职责收进
      // 「构建后推送」开关。四者的 spec 都必须留着:存量节点要能照常显示与执行。
      for (const legacy of ['build_image', 'build_frontend', 'build_backend', 'push_image']) {
        expect(PICKABLE_TYPES, legacy).not.toContain(legacy)
        expect(getJobTypeSpec(legacy), `${legacy} spec 不能删`).not.toBeNull()
      }
    })

    it('asks for the tier before anything else (no tier = no other field)', () => {
      const keys = visible({})
      expect(keys).toEqual(['artifactType'])
      // 档位下拉必须有明确的未选项:否则表单显示「镜像」而 config 里其实没档位。
      const tier = fields.find((f) => f.key === 'artifactType')
      expect(tier?.options?.[0]).toMatchObject({ value: '' })
    })

    it('image tier expands the Dockerfile subsection and the push switch', () => {
      const keys = visible({ artifactType: 'image' })
      expect(keys).toContain('buildModel')
      expect(keys).toContain('dockerfilePath')
      expect(keys).toContain('context')
      expect(keys).toContain('pushImage')
      // Dockerfile 的镜像由 FROM 决定,平台构建环境在此无关 —— 露出来等于骗人选。
      expect(keys).not.toContain('buildEnvId')
      expect(keys).not.toContain('commands')
    })

    it('image tier + toolchain model swaps to the preset build env', () => {
      const keys = visible({ artifactType: 'image', buildModel: 'toolchain' })
      expect(keys).toContain('buildEnvId')
      expect(keys).toContain('buildCommand')
      expect(keys).not.toContain('dockerfilePath')
      expect(keys).not.toContain('commands')
    })

    it('产物档位只有两档:下拉里不再按语言分档,历史值仍走脚本表单', () => {
      const tier = fields.find((f) => f.key === 'artifactType')
      expect(tier?.options?.map((o) => o.value)).toEqual(['', 'image', 'file'])
      // file 是新档位;jar / dist 是收敛前的取值 —— 存量节点不改配置也要展开同一套字段。
      for (const t of ['file', 'jar', 'dist']) {
        const keys = visible({ artifactType: t })
        expect(keys, t).toContain('buildEnvId')
        expect(keys, t).toContain('commands')
        expect(keys, t).toContain('artifactPath')
        expect(keys, t).toContain('cachePaths')
        expect(keys, t).not.toContain('dockerfilePath')
        expect(keys, t).not.toContain('buildModel')
        expect(keys, t).not.toContain('pushImage')
      }
      expect(normalizeArtifactTier('jar')).toBe('file')
      expect(normalizeArtifactTier('dist')).toBe('file')
      expect(normalizeArtifactTier('image')).toBe('image')
      // 只有构建档位要收敛:部署节点的 artifactType 是「产物偏好」,取值是运行期产物类型。
      expect(usesBuildTierField('build')).toBe(true)
      expect(usesBuildTierField('deploy_ssh')).toBe(false)
    })

    // 跨端契约:artifactType / pushImage 的键名与 pipeline.ConfigKey* 逐字一致。
    it('uses the exact key names the backend reads', () => {
      const keys = fields.map((f) => f.key)
      for (const k of ['artifactType', 'buildModel', 'dockerfilePath', 'context', 'buildEnvId', 'buildCommand', 'pushImage', 'commands', 'artifactPath']) {
        expect(keys, k).toContain(k)
      }
    })

    it('build templates prefill a tier plus its own fields', () => {
      expect(jobTemplatePrefill('build', 'frontend')).toMatchObject({ artifactType: 'file' })
      expect(jobTemplatePrefill('build', 'backend')).toMatchObject({ artifactType: 'file' })
      expect(jobTemplatePrefill('build', 'docker_image')).toMatchObject({ artifactType: 'image', buildModel: 'dockerfile' })
      expect(isScriptClassType('build', { artifactType: 'file' })).toBe(true)
      expect(isScriptClassType('build', { artifactType: 'image' })).toBe(false)
      expect(effectiveJobType('build', { artifactType: 'image' })).toBe('build_image')
      expect(effectiveJobType('build', { artifactType: 'file' })).toBe('script')
      expect(effectiveJobType('build', { artifactType: 'jar' })).toBe('script')
      expect(effectiveJobType('build', {})).toBe('build')
      expect(effectiveJobType('deploy_ssh', { artifactType: 'image' })).toBe('deploy_ssh')
    })
  })

  describe('build_image conditional fields', () => {
    const fields = JOB_TYPE_SPECS.build_image.fields

    function visible(config: Record<string, string>): string[] {
      return fields.filter((f) => !f.when || f.when(config)).map((f) => f.key)
    }

    it('shows Dockerfile fields by default (no model set)', () => {
      const keys = visible({})
      expect(keys).toContain('dockerfilePath')
      expect(keys).toContain('context')
      expect(keys).not.toContain('buildEnvId')
    })

    it('shows preset build env picker when model = toolchain', () => {
      const keys = visible({ buildModel: 'toolchain' })
      expect(keys).toContain('buildEnvId')
      expect(keys).toContain('buildCommand')
      expect(keys).not.toContain('dockerfilePath')
    })
  })

  describe('schemaKeys / splitConfig', () => {
    it('owns its declared keys', () => {
      const keys = schemaKeys('push_image')
      expect(keys.has('pushTarget')).toBe(true)
      expect(keys.has('nonexistent')).toBe(false)
    })

    it('returns an empty key set for unknown types', () => {
      expect(schemaKeys('unknown').size).toBe(0)
    })

    it('splits owned keys out and keeps unknown keys as extras', () => {
      const config = { myCustomFlag: 'x', another: 'y' }
      const { extras } = splitConfig('push_image', config)
      const extraKeys = extras.map(([k]) => k)
      expect(extraKeys).toEqual(['myCustomFlag', 'another'])
    })

    // R5:手输 registry 的四个字段是假字段(执行侧无人消费),既不进表单也不进「原始参数」。
    it('drops the legacy push_image fake fields entirely', () => {
      const config = {
        registry: 'harbor.local', imageName: 'org/app', tag: 'v1', credentialId: 'cred-1',
        myCustomFlag: 'x',
      }
      const { extras } = splitConfig('push_image', config)
      expect(extras.map(([k]) => k)).toEqual(['myCustomFlag'])
      for (const k of ['registry', 'imageName', 'tag', 'credentialId']) {
        expect(droppedKeys('push_image').has(k), k).toBe(true)
      }
    })

    it('treats every key as an extra for an unknown type', () => {
      const config = { a: '1', b: '2' }
      const { extras } = splitConfig('unknown', config)
      expect(extras.map(([k]) => k)).toEqual(['a', 'b'])
    })

    // R4:镜像桥接键(buildenv 控件写回的 image / toolchain*)能在「原始参数」里
    // 被手改,就等于留了个手填镜像的入口 —— 必须既不进表单也不进 extras。
    it('hides build-env bridge keys from raw extras but keeps them off the form', () => {
      const config = { image: 'node:20-alpine', commands: 'npm ci', myFlag: 'x' }
      const { extras } = splitConfig('script', config)
      expect(extras.map(([k]) => k)).toEqual(['myFlag'])
      const managed = managedKeys('script')
      expect(managed.has('image')).toBe(true)
      expect(schemaKeys('script').has('image')).toBe(false)
    })

    it('hides toolchain bridge keys for build_image', () => {
      const config = {
        buildModel: 'toolchain',
        toolchainLanguage: 'node',
        toolchainVersion: '20-alpine',
        image: 'node:20-alpine',
        buildCommand: 'npm ci',
      }
      const { extras } = splitConfig('build_image', config)
      expect(extras).toEqual([])
      for (const k of ['image', 'toolchainLanguage', 'toolchainVersion']) {
        expect(managedKeys('build_image').has(k), k).toBe(true)
      }
    })

    it('leaves the templated node image editable (it must be able to hold {{param}})', () => {
      expect(schemaKeys('templated').has('image')).toBe(true)
      expect(managedKeys('templated').has('image')).toBe(false)
    })
  })

  describe('默认配置不写死镜像(R4:镜像只能来自预置目录)', () => {
    it('every defaultConfig key is rendered by the form or managed by the build env control', () => {
      for (const spec of Object.values(JOB_TYPE_SPECS)) {
        const allowed = new Set([...schemaKeys(spec.type), ...managedKeys(spec.type)])
        for (const key of Object.keys(spec.defaultConfig ?? {})) {
          expect(allowed.has(key), `${spec.type} default writes unmanaged key ${key}`).toBe(true)
        }
        expect(spec.defaultConfig?.image, `${spec.type} must not prefill a literal image`).toBeUndefined()
      }
    })
  })

  describe('build cache fields (build cache · P0)', () => {
    // 脚本类节点(后端 isScriptJob 路径)都暴露 cachePaths/cacheKey 表单字段。
    // build 也在其中:它的 jar/dist 档位就是脚本路径(缓存对镜像档位无意义,故 gated 掉)。
    const SCRIPT_CLASS = ['script', 'custom', 'build_frontend', 'build_backend', 'templated', 'build']

    it('script-class types expose cachePaths/cacheKey fields', () => {
      for (const t of SCRIPT_CLASS) {
        const keys = JOB_TYPE_SPECS[t].fields.map((f) => f.key)
        expect(keys, `${t} should offer cachePaths`).toContain('cachePaths')
        expect(keys, `${t} should offer cacheKey`).toContain('cacheKey')
      }
    })

    it('owns cache keys (not dropped as raw extras)', () => {
      for (const t of SCRIPT_CLASS) {
        const keys = schemaKeys(t)
        expect(keys.has('cachePaths'), `${t} owns cachePaths`).toBe(true)
        expect(keys.has('cacheKey'), `${t} owns cacheKey`).toBe(true)
        const { extras } = splitConfig(t, { cachePaths: 'node_modules', cacheKey: 'k' })
        expect(extras.length, `${t} keeps cache keys typed, not extras`).toBe(0)
      }
    })

    it('non-script types do not gain cache fields', () => {
      const keys = JOB_TYPE_SPECS.git_source.fields.map((f) => f.key)
      expect(keys).not.toContain('cachePaths')
    })
  })
})
