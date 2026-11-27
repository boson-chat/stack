import { describe, expect, it } from 'vitest';
import { ago, country, flag, num, pct, range } from './fmt.js';

describe('fmt', () => {
	it('formats numbers and shares', () => {
		expect(num(12345)).toBe('12,345');
		expect(num(null)).toBe('–');
		expect(pct(1, 3)).toBe('33%');
		expect(pct(1, 1000)).toBe('<1%');
		expect(pct(0, 0)).toBe('0%');
	});
	it('sums bucket ranges', () => {
		expect(range({ lo: 12, hi: 60, reported: 2, at_least: false }).main).toBe('~36');
		expect(range({ lo: 5, hi: 5, reported: 5, at_least: false }).main).toBe('5');
		expect(range({ lo: 5001, hi: 5001, reported: 1, at_least: true }).main).toBe('5,001+');
		expect(range({ lo: 5012, hi: 5060, reported: 2, at_least: true }).main).toBe('≥ 5,012');
		expect(range({ lo: 0, hi: 0, reported: 0 }).main).toBe('–');
	});
	it('names countries', () => {
		expect(country('DE')).toBe('Germany');
		expect(country('ZZ')).toBe('Unknown');
		expect(flag('DE')).toBe('🇩🇪');
		expect(flag('ZZ')).toBe('');
	});
	it('says how long ago', () => {
		const now = Date.parse('2026-11-27T12:00:00Z');
		expect(ago('2026-11-27T11:59:30Z', now)).toBe('just now');
		expect(ago('2026-11-27T11:00:00Z', now)).toBe('1 h ago');
		expect(ago('2026-11-26T12:00:00Z', now)).toBe('24 h ago');
		expect(ago('2026-11-20T12:00:00Z', now)).toBe('7 days ago');
	});
});
