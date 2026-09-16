import { it } from 'vitest';
import { validateYAML } from './validate.ts';

it('dbg', () => {
  console.log('A', JSON.stringify(validateYAML('- hosts: all\n  tasks: []\n')));
  console.log('D', JSON.stringify(validateYAML('- hosts: web\n  vars:\n    x: 1\n  tasks: []\n- hosts: db\n  tasks: []\n')));
  console.log('E', JSON.stringify(validateYAML('- hosts: 123\n')));
  console.log('F', JSON.stringify(validateYAML('- hosts: all\n---\ngarbage\n')));
  console.log('G', JSON.stringify(validateYAML('- {hosts: all, tasks: []}\n')));
  console.log('H', JSON.stringify(validateYAML('- hosts: "web:80"\n  tasks: []\n')));
  console.log('I', JSON.stringify(validateYAML('# my playbook\n- hosts: all # inline\n  tasks: []\n')));
  console.log('J', JSON.stringify(validateYAML('# x'.padEnd(0) + '- hosts: all\n# ' + 'x'.repeat(512 * 1024 - 16) + '\n')));
});
