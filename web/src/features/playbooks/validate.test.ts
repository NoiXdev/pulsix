import { describe, expect, it } from 'vitest';
import { validateYAML } from './validate.ts';

/** The same table internal/playbooks/validate_test.go runs, with the
 * messages shown to the operator as-is. */
describe('validateYAML mirrors playbooks.ValidateYAML', () => {
  it('accepts a valid single play', () => {
    expect(validateYAML('- hosts: all\n  tasks: []\n')).toBeNull();
  });

  it('accepts a valid multi-play playbook with nested vars', () => {
    expect(validateYAML('- hosts: web\n  vars:\n    x: 1\n  tasks: []\n- hosts: db\n  tasks: []\n')).toBeNull();
  });

  it('rejects a document that is not a list', () => {
    expect(validateYAML('hosts: all\n')).toBe('expected a list of plays');
  });

  it('rejects a scalar document', () => {
    expect(validateYAML('42\n')).toBe('expected a list of plays');
  });

  it('rejects an empty document', () => {
    expect(validateYAML('')).toBe('expected a list of plays');
  });

  it('rejects an empty list', () => {
    expect(validateYAML('[]\n')).toBe('expected a list of plays');
  });

  it('rejects a first play that is not a mapping', () => {
    expect(validateYAML('- x\n')).toBe('play 1: must be a mapping');
  });

  it('rejects a play without hosts', () => {
    expect(validateYAML('- name: x\n  tasks: []\n')).toBe('play 1: missing hosts');
  });

  it('rejects an empty hosts value', () => {
    expect(validateYAML('- hosts: ""\n  tasks: []\n')).toBe('play 1: missing hosts');
  });

  it('rejects a hosts value that is not a string', () => {
    expect(validateYAML('- hosts: 123\n  tasks: []\n')).toBe('play 1: hosts must be a string');
  });

  it('reports malformed YAML with a cause for tab indentation', () => {
    expect(validateYAML('- hosts: all\n\ttasks: []\n')).toMatch(/^malformed YAML: /);
  });

  it('considers the first document only', () => {
    expect(validateYAML('- hosts: all\n---\ngarbage\n')).toBeNull();
  });

  it('accepts flow maps and quoted hosts', () => {
    expect(validateYAML('- {hosts: all, tasks: []}\n')).toBeNull();
    expect(validateYAML('- hosts: "web:80"\n  tasks: []\n')).toBeNull();
  });

  it('accepts comments and directives', () => {
    expect(validateYAML('# my playbook\n- hosts: all # inline\n  tasks: []\n')).toBeNull();
  });

  it('leaves constructs outside the subset to the server', () => {
    expect(validateYAML('- hosts: all\n  vars: &anchor\n    x: 1\n  tasks: []\n')).toBeNull();
    expect(validateYAML('- hosts: all\n  tasks:\n  - debug:\n      msg: |\n          hello\n')).toBeNull();
  });

  it('pins the 512 KiB byte boundary', () => {
    const atLimit = '- hosts: all\n# ' + 'x'.repeat(512 * 1024 - '- hosts: all\n# '.length - 1) + '\n';
    expect(new TextEncoder().encode(atLimit).length).toBe(512 * 1024);
    expect(validateYAML(atLimit)).toBeNull();
    expect(validateYAML(atLimit + 'x')).toBe('playbook exceeds 512 KiB');
  });
});
