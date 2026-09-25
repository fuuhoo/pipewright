import { describe, it, expect } from 'vitest'
import { countBy, edgePath, fanAnchor, type EdgeBox } from './dagEdges'

const box = (x: number, y: number, w: number, h: number): EdgeBox => ({ x, y, w, h })

describe('dagEdges', () => {
  describe('fanAnchor', () => {
    it('single edge sits on the vertical center', () => {
      expect(fanAnchor(box(0, 100, 200, 60), 0, 1)).toBe(130)
    })
    it('splits a side so fan-in edges never land on one point', () => {
      const b = box(0, 100, 200, 60)
      expect([0, 1, 2].map((i) => fanAnchor(b, i, 3))).toEqual([115, 130, 145])
    })
    it('keeps every anchor inside the box', () => {
      const b = box(0, 0, 200, 44)
      for (let i = 0; i < 6; i++) {
        const y = fanAnchor(b, i, 6)
        expect(y).toBeGreaterThan(b.y)
        expect(y).toBeLessThan(b.y + b.h)
      }
    })
  })

  describe('edgePath', () => {
    it('leaves the right edge and enters the left edge at the given heights', () => {
      const d = edgePath(box(0, 0, 200, 60), box(300, 100, 200, 60), 30, 130)
      expect(d.startsWith('M200,30 ')).toBe(true)
      expect(d.endsWith('300,130')).toBe(true)
    })
    it('keeps a minimum horizontal control offset for short edges', () => {
      const d = edgePath(box(0, 0, 200, 60), box(210, 0, 200, 60), 30, 30, 18)
      expect(d).toBe('M200,30 C218,30 192,30 210,30')
    })
    it('scales the control offset with the gap for long edges', () => {
      const d = edgePath(box(0, 0, 200, 60), box(400, 0, 200, 60), 30, 30, 18)
      expect(d).toBe('M200,30 C300,30 300,30 400,30')
    })
  })

  describe('countBy', () => {
    it('counts edges per node on one side', () => {
      const m = countBy(['a', 'b', 'a', 'a'])
      expect(m.get('a')).toBe(3)
      expect(m.get('b')).toBe(1)
      expect(m.get('missing')).toBeUndefined()
    })
  })
})
