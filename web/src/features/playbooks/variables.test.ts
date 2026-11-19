import { describe, expect, it } from 'vitest';
import { playbookVariables, type PlaybookVariable } from './variables.ts';

const lines = (...l: string[]) => l.join('\n');

describe('playbookVariables', () => {
  const cases: { name: string; yaml: string; want: PlaybookVariable[] }[] = [
    { name: 'nothing in an empty document', yaml: '', want: [] },
    {
      name: 'a plain name needs a value from outside',
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ greeting }}"'),
      want: [{ name: 'greeting', source: 'external' }],
    },
    {
      name: 'only the top-level identifier of an attribute and a filter',
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', "        msg: \"{{ app.version | default('1') }} {{ site['url'] }}\""),
      want: [
        { name: 'app', source: 'external' },
        { name: 'site', source: 'external' },
      ],
    },
    {
      name: 'deduplicated, in the order of first use',
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ b }}{{ a }}{{ b }}"', '    - ansible.builtin.debug:', '        msg: "{{a}}"'),
      want: [
        { name: 'b', source: 'external' },
        { name: 'a', source: 'external' },
      ],
    },
    {
      name: "a play's own vars mapping sets a name, its values may use others",
      yaml: lines(
        '- name: Deploy',
        '  hosts: web',
        '  vars:',
        '    app_version: "{{ release }}"',
        '    port: 8080',
        '  tasks:',
        '    - ansible.builtin.git:',
        '        repo: "{{ repo_url }}"',
        '        version: "{{ app_version }}:{{ port }}"',
      ),
      want: [
        { name: 'release', source: 'external' },
        { name: 'repo_url', source: 'external' },
        { name: 'app_version', source: 'vars' },
        { name: 'port', source: 'vars' },
      ],
    },
    {
      name: 'a flow mapping of vars counts too',
      yaml: lines('- hosts: all', '  vars: { a: 1, "b": "{{ c }}" }', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ a }}{{ b }}"'),
      want: [
        { name: 'c', source: 'external' },
        { name: 'a', source: 'vars' },
        { name: 'b', source: 'vars' },
      ],
    },
    {
      name: "vars as the play's first key, after the dash",
      yaml: lines('- vars:', '    x: 1', '  hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ x }}"'),
      want: [{ name: 'x', source: 'vars' }],
    },
    {
      name: "a task's vars are not the play's",
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ inner }}"', '      vars:', '        inner: 1'),
      want: [{ name: 'inner', source: 'external' }],
    },
    {
      name: "one play's vars do not reach another play",
      yaml: lines(
        '- hosts: a',
        '  vars:',
        '    shared: 1',
        '  tasks:',
        '    - ansible.builtin.debug:',
        '        msg: "{{ shared }}"',
        '- hosts: b',
        '  tasks:',
        '    - ansible.builtin.debug:',
        '        msg: "{{ shared }}"',
      ),
      want: [{ name: 'shared', source: 'external' }],
    },
    {
      name: 'a registered result and a set_fact are set by a task',
      yaml: lines(
        '- hosts: all',
        '  tasks:',
        '    - ansible.builtin.command: uptime',
        '      register: up',
        '    - ansible.builtin.set_fact:',
        '        when_up: "{{ up.stdout }}"',
        '        cacheable: true',
        '    - ansible.builtin.debug:',
        '        msg: "{{ when_up }}"',
      ),
      want: [
        { name: 'up', source: 'task' },
        { name: 'when_up', source: 'task' },
      ],
    },
    {
      name: 'statements, Jinja comments and YAML comments are ignored',
      yaml: lines(
        '# {{ in_a_comment }}',
        '- hosts: all',
        '  tasks:',
        '    - ansible.builtin.debug:',
        '        msg: "{% if flag %}{{ shown }}{% endif %}{# {{ hidden }} #}" # {{ trailing }}',
      ),
      want: [{ name: 'shown', source: 'external' }],
    },
    {
      name: "Ansible's own names, loop variables, literals and calls are left out",
      yaml: lines(
        '- hosts: all',
        '  tasks:',
        '    - ansible.builtin.debug:',
        "        msg: \"{{ item }} {{ ansible_facts.os_family }} {{ inventory_hostname }} {{ hostvars[x] }} {{ lookup('env', 'HOME') }} {{ 'text' }} {{ 42 }} {{ true }} {{ pkg }}\"",
        '      loop: [1]',
        '      loop_control:',
        '        loop_var: pkg',
      ),
      want: [],
    },
    {
      name: 'the first name after parentheses and a keyword',
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ (count | int) + 1 }} {{ not enabled }}"'),
      want: [
        { name: 'count', source: 'external' },
        { name: 'enabled', source: 'external' },
      ],
    },
    {
      name: 'an unclosed expression is no name',
      yaml: lines('- hosts: all', '  tasks:', '    - ansible.builtin.debug:', '        msg: "{{ broken"'),
      want: [],
    },
  ];

  it.each(cases)('$name', ({ yaml, want }) => {
    expect(playbookVariables(yaml)).toEqual(want);
  });
});
