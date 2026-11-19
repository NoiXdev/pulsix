import { describe, expect, it } from 'vitest';
import { firstPlayName } from './playName.ts';

describe('firstPlayName', () => {
  it.each([
    ['', ''],
    ['- hosts: all\n', ''],
    ['- name: Deploy web\n  hosts: all\n', 'Deploy web'],
    ['---\n# a comment\n- hosts: all\n  name: "Quoted name"\n', 'Quoted name'],
    ["- hosts: all\n  name: 'single'\n", 'single'],
    ['- hosts: all\n  tasks:\n    - name: a task\n- name: second play\n', ''],
    ['hosts: all\nname: not a play\n', ''],
  ])('reads %j as %j', (yaml, want) => {
    expect(firstPlayName(yaml)).toBe(want);
  });
});
