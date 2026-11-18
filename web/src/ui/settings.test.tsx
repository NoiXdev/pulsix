import { describe, expect, it, vi } from 'vitest';
import { act, render, renderHook, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryHistory, createRootRoute, createRouter, RouterContextProvider } from '@tanstack/react-router';
import type { ReactNode } from 'react';
import { ConfirmProvider } from './confirm.tsx';
import { CardSaveBar, PageHeader, SettingsCard, SettingsPage, SettingsRow } from './settings.tsx';
import { useCardForm } from './useCardForm.ts';

describe('PageHeader', () => {
  it('renders the title as h1 with description, badge and actions', () => {
    render(<PageHeader title="Security" description="Keep it safe" badge={<span>Beta</span>} actions={<button>Act</button>} />);
    expect(screen.getByRole('heading', { name: 'Security', level: 1 })).toBeInTheDocument();
    expect(screen.getByText('Keep it safe')).toBeInTheDocument();
    expect(screen.getByText('Beta')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Act' })).toBeInTheDocument();
  });
});

describe('SettingsPage', () => {
  it('wraps the children in a column under the header', () => {
    render(
      <SettingsPage title="Account">
        <p>inside</p>
      </SettingsPage>,
    );
    expect(screen.getByRole('heading', { name: 'Account', level: 1 })).toBeInTheDocument();
    expect(screen.getByText('inside').parentElement).toHaveClass('max-w-[880px]', 'gap-5');
  });
});

describe('SettingsCard', () => {
  it('is a data-card section labelled by its title, with description, badge and footer last', () => {
    render(
      <SettingsCard title="Profile" description="Who you are" badge={<span>On</span>} footer={<div>the footer</div>}>
        <div>body</div>
      </SettingsCard>,
    );
    const card = screen.getByRole('region', { name: 'Profile' });
    expect(card).toHaveAttribute('data-card');
    expect(screen.getByText('Who you are')).toBeInTheDocument();
    expect(screen.getByText('On')).toBeInTheDocument();
    expect(card.lastElementChild).toHaveTextContent('the footer');
  });

  it('uses a given headingId', () => {
    render(<SettingsCard title="Profile" headingId="h-profile" />);
    expect(screen.getByText('Profile')).toHaveAttribute('id', 'h-profile');
  });
});

describe('SettingsRow', () => {
  it('associates the label with the control and shows help', () => {
    render(
      <SettingsRow label="Name" htmlFor="n" help="Shown to others">
        <input id="n" />
      </SettingsRow>,
    );
    expect(screen.getByLabelText('Name')).toBe(document.getElementById('n'));
    expect(screen.getByText('Shown to others')).toBeInTheDocument();
  });

  it('exposes a labelId for aria-labelledby when there is no htmlFor', () => {
    render(
      <SettingsRow label="Mode" labelId="mode-l">
        <div role="group" aria-labelledby="mode-l" />
      </SettingsRow>,
    );
    expect(screen.getByRole('group', { name: 'Mode' })).toBeInTheDocument();
  });
});

describe('CardSaveBar', () => {
  it('disables Save and hides the dirty hint when clean', () => {
    render(<CardSaveBar dirty={false} saving={false} onDiscard={() => {}} />);
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
  });

  it('shows the hint and Discard when dirty, and Discard calls back', async () => {
    const onDiscard = vi.fn();
    render(<CardSaveBar dirty saving={false} onDiscard={onDiscard} formId="f" saveLabel="Apply" />);
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument();
    const save = screen.getByRole('button', { name: 'Apply' });
    expect(save).toBeEnabled();
    expect(save).toHaveAttribute('type', 'submit');
    expect(save).toHaveAttribute('form', 'f');
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(onDiscard).toHaveBeenCalledOnce();
  });

  it('disables Save while saving', () => {
    render(<CardSaveBar dirty saving onDiscard={() => {}} />);
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });
});

function renderForm<T>(initial: T) {
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  });
  function wrapper({ children }: { children: ReactNode }) {
    return (
      <ConfirmProvider>
        <RouterContextProvider router={router}>{children}</RouterContextProvider>
      </ConfirmProvider>
    );
  }
  return renderHook(({ saved }: { saved: T }) => useCardForm(saved), { wrapper, initialProps: { saved: initial } });
}

describe('useCardForm', () => {
  it('flips dirty on change and back on discard', () => {
    const { result } = renderForm({ a: 1 });
    expect(result.current.dirty).toBe(false);
    act(() => result.current.setDraft({ a: 2 }));
    expect(result.current.dirty).toBe(true);
    act(() => result.current.discard());
    expect(result.current.draft).toEqual({ a: 1 });
    expect(result.current.dirty).toBe(false);
  });

  it('accepts an updater and compares structurally', () => {
    const { result } = renderForm({ a: 1 });
    act(() => result.current.setDraft((d) => ({ a: d.a + 1 })));
    expect(result.current.dirty).toBe(true);
    act(() => result.current.setDraft({ a: 1 }));
    expect(result.current.dirty).toBe(false);
  });

  it('markSaved makes the draft the new baseline', () => {
    const { result } = renderForm({ a: 1 });
    act(() => result.current.setDraft({ a: 5 }));
    act(() => result.current.markSaved());
    expect(result.current.dirty).toBe(false);
    act(() => result.current.setDraft({ a: 6 }));
    act(() => result.current.discard());
    expect(result.current.draft).toEqual({ a: 5 });
    act(() => result.current.markSaved({ a: 9 }));
    expect(result.current.draft).toEqual({ a: 9 });
    expect(result.current.dirty).toBe(false);
  });

  it('follows a refetched saved value while clean, but not while dirty', () => {
    const { result, rerender } = renderForm({ a: 1 });
    rerender({ saved: { a: 2 } });
    expect(result.current.draft).toEqual({ a: 2 });
    expect(result.current.dirty).toBe(false);
    act(() => result.current.setDraft({ a: 7 }));
    rerender({ saved: { a: 3 } });
    expect(result.current.draft).toEqual({ a: 7 });
    expect(result.current.dirty).toBe(true);
  });

  it('keeps the draft when saved changes identity but not content', () => {
    const { result, rerender } = renderForm({ a: 1 });
    act(() => result.current.setDraft({ a: 4 }));
    rerender({ saved: { a: 1 } });
    expect(result.current.draft).toEqual({ a: 4 });
  });
});
