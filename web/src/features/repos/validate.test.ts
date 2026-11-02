import { describe, expect, it } from 'vitest';
import { validateRepoInput as validate, type RepoFormValues } from './validate.ts';
import { en } from '../../i18n/en/index.ts';

const validateRepoInput = (values: RepoFormValues) => validate(values, en);

describe('validateRepoInput mirrors the server', () => {
  it('accepts an https URL and defaults-free form', () => {
    expect(validateRepoInput({ name: 'infra', url: 'https://git.example/acme/infra.git', branch: 'main', subpath: '' })).toEqual({});
  });

  it('accepts an ssh:// URL and an SCP-like git URL', () => {
    expect(validateRepoInput({ name: 'infra', url: 'ssh://git@git.example/acme/infra.git', branch: '', subpath: '' }).url).toBeUndefined();
    expect(validateRepoInput({ name: 'infra', url: 'git@git.example:acme/infra.git', branch: '', subpath: '' }).url).toBeUndefined();
  });

  it('rejects a bad name with the server message', () => {
    expect(validateRepoInput({ name: 'Infra!', url: 'https://x.example/a.git', branch: '', subpath: '' }).name).toBe(
      'invalid name: must be lowercase letters, digits, dots, underscores or hyphens (1–63 characters), starting with a letter or digit',
    );
  });

  it('rejects a non-git URL', () => {
    expect(validateRepoInput({ name: 'infra', url: 'ftp://x.example/a.git', branch: '', subpath: '' }).url).toBe(
      'invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL',
    );
    expect(validateRepoInput({ name: 'infra', url: '', branch: '', subpath: '' }).url).toBe(
      'invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL',
    );
    expect(validateRepoInput({ name: 'infra', url: 'https://x.example/a b.git', branch: '', subpath: '' }).url).toBe(
      'invalid url: must not contain whitespace',
    );
  });

  it('rejects a branch with ..', () => {
    expect(validateRepoInput({ name: 'infra', url: 'https://x.example/a.git', branch: 'a..b', subpath: '' }).branch).toBe(
      'invalid branch: may contain letters, digits, dots, slashes and hyphens (1–100 characters), no ".."',
    );
  });

  it('rejects a subpath with ..', () => {
    expect(validateRepoInput({ name: 'infra', url: 'https://x.example/a.git', branch: '', subpath: '../etc' }).subpath).toBe(
      'invalid subpath: may be empty or contain letters, digits, dots, slashes and hyphens (up to 200 characters), no ".."',
    );
  });
});
