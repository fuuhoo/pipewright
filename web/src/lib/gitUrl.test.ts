import { describe, expect, it } from 'vitest'

import { isSupportedRepoUrl } from './gitUrl'

describe('isSupportedRepoUrl', () => {
  it('accepts http(s) and ssh URLs and scp syntax', () => {
    for (const url of [
      'https://github.com/acme/app.git',
      'http://192.168.1.10:3000/acme/app.git',
      'ssh://git@172.17.4.41:5052/fuuhoo/sw-lowcode.git',
      'git@172.17.4.41:fuuhoo/sw-lowcode.git',
      'git@gitee.com:cool-jiawei/aireboot.git',
    ]) {
      expect(isSupportedRepoUrl(url)).toBe(true)
    }
  })

  it('rejects empty, local paths and unsupported schemes', () => {
    for (const url of ['', '   ', '/tmp/repo', 'file:///etc/passwd', 'git://host/repo.git', 'C:\\repos\\app']) {
      expect(isSupportedRepoUrl(url)).toBe(false)
    }
  })
})
