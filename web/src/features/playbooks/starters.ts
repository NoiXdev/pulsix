/** A starting point the new playbook page fills the editor with. Its label
 * and description are the catalogue's (`t.playbooks.newPage.starters`). */
export interface PlaybookStarter {
  id: 'empty' | 'packages' | 'template' | 'service';
  yaml: string;
}

/**
 * The new page's four starters. They are ordinary, valid playbooks with
 * fully qualified module names; the values to change are variables, so the
 * page's Variables hint lists them.
 */
export const PLAYBOOK_STARTERS: readonly PlaybookStarter[] = [
  {
    id: 'empty',
    yaml: `- name: Describe what this play does
  hosts: all
  become: true
  tasks:
    - name: Say hello
      ansible.builtin.debug:
        msg: "Hello from {{ inventory_hostname }}"
`,
  },
  {
    id: 'packages',
    yaml: `- name: Install packages
  hosts: all
  become: true
  vars:
    packages:
      - curl
      - git
  tasks:
    - name: Install the packages with the system's package manager
      ansible.builtin.package:
        name: "{{ packages }}"
        state: present
`,
  },
  {
    id: 'template',
    yaml: `- name: Render a configuration file
  hosts: all
  become: true
  tasks:
    - name: Render the file from its template
      ansible.builtin.template:
        src: "{{ config_template }}"
        dest: "{{ config_path }}"
        owner: root
        group: root
        mode: "0644"
      notify: Restart the service

  handlers:
    - name: Restart the service
      ansible.builtin.service:
        name: "{{ service_name }}"
        state: restarted
`,
  },
  {
    id: 'service',
    yaml: `- name: Restart a service
  hosts: all
  become: true
  tasks:
    - name: Restart the service
      ansible.builtin.service:
        name: "{{ service_name }}"
        state: restarted

    - name: Read the state of the services
      ansible.builtin.service_facts:

    - name: Check that the service runs
      ansible.builtin.assert:
        that:
          - ansible_facts.services[service_name + '.service'].state == 'running'
        fail_msg: "{{ service_name }} is not running"
`,
  },
];
