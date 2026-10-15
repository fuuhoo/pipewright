import { describe, expect, it } from 'vitest'

import {
  PROFILE_EMAIL_MAX,
  PROFILE_NAME_MAX,
  collapseWhitespace,
  contactOf,
  emailProblem,
  handleOf,
  initialsOf,
  nicknameOf,
  normalizeDisplayName,
  primaryNameOf,
} from './userDisplay'

describe('collapseWhitespace / normalizeDisplayName', () => {
  it('折叠内部连续空白并去首尾(与后端同一算法)', () => {
    expect(collapseWhitespace('  傅   兵\n测试 ')).toBe('傅 兵 测试')
    expect(collapseWhitespace('   ')).toBe('')
  })

  it('超过上限按码点截断,不到上限原样返回', () => {
    const long = 'a'.repeat(PROFILE_NAME_MAX + 10)
    expect(normalizeDisplayName(long)).toHaveLength(PROFILE_NAME_MAX)
    const cjk = '名'.repeat(PROFILE_NAME_MAX + 5)
    expect(normalizeDisplayName(cjk)).toHaveLength(PROFILE_NAME_MAX)
    expect(normalizeDisplayName('  Dave  ')).toBe('Dave')
  })

  it('代理对算一个字符,不会被切成半个', () => {
    const emoji = '😀'.repeat(PROFILE_NAME_MAX + 2)
    const got = normalizeDisplayName(emoji)
    expect(Array.from(got)).toHaveLength(PROFILE_NAME_MAX)
    expect(got).not.toContain('\uFFFD')
  })
})

describe('三件套的回落', () => {
  it('没昵称时主标签回登录名,有昵称时用昵称', () => {
    expect(primaryNameOf({ username: 'dave', displayName: '' })).toBe('dave')
    expect(primaryNameOf({ username: 'dave', displayName: '  大伟 ' })).toBe('大伟')
    expect(primaryNameOf({ username: 'dave' })).toBe('dave')
  })

  it('nicknameOf 给出归一化后的昵称(可能为空,调用方自己回落)', () => {
    expect(nicknameOf({ username: 'dave', displayName: 'A  B' })).toBe('A B')
    expect(nicknameOf({ username: 'dave', displayName: '   ' })).toBe('')
  })

  it('次行:有邮箱用邮箱,没有退回 @登录名', () => {
    expect(contactOf({ username: 'dave', email: 'dave@corp.co' })).toBe('dave@corp.co')
    expect(contactOf({ username: 'dave', email: '  ' })).toBe('@dave')
    expect(handleOf({ username: 'dave' })).toBe('@dave')
  })

  it('头像字头跟主标签走,不是永远用登录名', () => {
    expect(initialsOf({ username: 'dave' })).toBe('D')
    expect(initialsOf({ username: 'dave', displayName: '大伟' })).toBe('大')
    expect(initialsOf({ username: '', displayName: '' })).toBe('?')
  })
})

describe('emailProblem 与后端 normalizeEmail 同判据', () => {
  it.each([
    [''],
    ['   '],
    ['a@b'],
    ['dave@corp.co'],
    ['x'.repeat(PROFILE_EMAIL_MAX - 3) + '@b'],
  ])('放行 %j', (value) => {
    expect(emailProblem(value)).toBe('')
  })

  it.each([
    ['dave @corp.co', 'space'],
    ['da\tve@corp.co', 'space'],
    ['@corp.co', 'format'],
    ['dave@', 'format'],
    ['a@b@c.co', 'format'],
    ['x'.repeat(PROFILE_EMAIL_MAX + 1) + '@b.co', 'tooLong'],
  ] as const)('拒绝 %j → %s', (value, want) => {
    expect(emailProblem(value)).toBe(want)
  })
})
