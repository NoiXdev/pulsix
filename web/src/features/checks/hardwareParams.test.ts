import { describe, expect, it } from 'vitest';
import { en } from '../../i18n/en/index.ts';
import { hardwareErrors, hardwareParams, isHardwarePlugin } from './hardwareParams.ts';

describe('isHardwarePlugin', () => {
  it.each([['temperature', true], ['smart', true], ['raid', true], ['disk', false], ['docker', false]])('%s → %s', (p, want) =>
    expect(isHardwarePlugin(p)).toBe(want));
});

describe('hardwareParams', () => {
  it.each([
    ['temperature', '', {}],
    ['temperature', ' \n \n', {}],
    ['temperature', 'coretemp_core_0\n nvme_composite \n', { sensors: ['coretemp_core_0', 'nvme_composite'] }],
    ['smart', '/dev/sda\n/dev/nvme0', { devices: ['/dev/sda', '/dev/nvme0'] }],
    ['raid', 'md0', { arrays: ['md0'] }],
    ['raid', '', {}],
  ])('%s %j → %j', (plugin, text, want) => {
    expect(hardwareParams(plugin as 'raid', text)).toEqual(want);
  });
});

describe('hardwareErrors', () => {
  const big = 'x'.repeat(256);
  it.each([
    ['empty is fine', 'temperature', '', false],
    ['a normal sensor', 'temperature', 'coretemp core 0', false],
    ['255 bytes pass', 'temperature', 'x'.repeat(255), false],
    ['256 bytes fail', 'temperature', big, true],
    ['128 two-byte characters are 256 bytes', 'temperature', 'ä'.repeat(128), true],
    ['a control character fails', 'raid', 'md\u00070', true],
    ['64 entries pass', 'raid', Array.from({ length: 64 }, (_, i) => `md${i}`).join('\n'), false],
    ['65 entries fail', 'raid', Array.from({ length: 65 }, (_, i) => `md${i}`).join('\n'), true],
    ['a device path passes', 'smart', '/dev/disk/by-id/ata-X_1,2:3+4@5', false],
    ['a bare name fails', 'smart', 'sda', true],
    ['a space fails', 'smart', '/dev/sd a', true],
    ['just /dev/ fails', 'smart', '/dev/', true],
    ['a second bad line fails', 'smart', '/dev/sda\nnvme0', true],
    ['a 256-byte device fails', 'smart', `/dev/${'a'.repeat(251)}`, true],
    ['a .. segment fails', 'smart', '/dev/../x', true],
    ['a trailing .. segment fails', 'smart', '/dev/disk/..', true],
    ['dots inside a name pass', 'smart', '/dev/disk/by-id/nvme-eui.00..25', false],
    ['multibyte over 255 bytes fails', 'smart', `/dev/${'ä'.repeat(126)}`, true],
  ])('%s', (_n, plugin, text, bad) => {
    const errs = hardwareErrors(plugin as 'raid', text, en);
    expect(Object.keys(errs).length > 0).toBe(bad);
  });
});
