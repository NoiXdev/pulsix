import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { AssertionEditor } from './AssertionEditor.tsx';

/** The editor with the form's state around it: `text` is what the form
 * would send. */
function Harness({ initial, error, serverError }: { initial: string; error?: string; serverError?: string }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <AssertionEditor id="a" label="Assertions" value={value} error={error} serverError={serverError} onChange={setValue} />
      <output data-testid="text">{value}</output>
    </>
  );
}

const text = () => screen.getByTestId('text').textContent;
const options = () => screen.getAllByRole('option').map((o) => o.getAttribute('data-value'));

describe('AssertionEditor', () => {
  it('shows one row per entry with its parts', () => {
    render(<Harness initial={'ok=status in 200..299 @warn\nct=header("Content-Type") contains "json"\ne=json("$.error") !exists'} />);
    expect(screen.getAllByTestId('assertion-row')).toHaveLength(3);
    expect(screen.getByLabelText('Name of assertion 1')).toHaveValue('ok');
    expect(screen.getByLabelText('Subject of assertion 1')).toHaveValue('status (status code)');
    expect(screen.getByLabelText('Operator of assertion 1')).toHaveValue('in');
    expect(screen.getByLabelText('Lowest value of assertion 1')).toHaveValue('200');
    expect(screen.getByLabelText('Highest value of assertion 1')).toHaveValue('299');
    expect(screen.getByLabelText('Severity of assertion 1')).toHaveValue('warn');
    expect(screen.getByLabelText('Argument of assertion 2')).toHaveValue('Content-Type');
    expect(screen.getByLabelText('Value of assertion 2')).toHaveValue('json');
    expect(screen.getByLabelText('Severity of assertion 2')).toHaveValue('crit');
    // exists and !exists take no value.
    expect(screen.queryByLabelText('Value of assertion 3')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Argument of assertion 1')).not.toBeInTheDocument();
  });

  it('composes the canonical text from a new row', async () => {
    const user = userEvent.setup();
    render(<Harness initial="" />);
    expect(screen.getByText('No assertions: the check reports up or down only.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Add assertion' }));
    expect(text()).toBe('');
    await user.type(screen.getByLabelText('Name of assertion 1'), 'title');
    await pickOption(screen, 'Subject of assertion 1', { value: 'html' });
    await user.type(screen.getByLabelText('Argument of assertion 1'), 'h1.title');
    await pickOption(screen, 'Operator of assertion 1', { value: 'contains' });
    await user.type(screen.getByLabelText('Value of assertion 1'), 'Say "hi"');
    await pickOption(screen, 'Severity of assertion 1', { value: 'warn' });
    expect(text()).toBe('title=html("h1.title") contains "Say \\"hi\\"" @warn');
    // The html(…) selector subset is named while an html row is there.
    expect(screen.getByText(/html\(…\) takes a CSS selector from this subset/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Remove assertion 1' }));
    expect(text()).toBe('');
  });

  it('offers only the operators the subject takes', async () => {
    const user = userEvent.setup();
    render(<Harness initial="ok=status == 200" />);
    await user.click(screen.getByLabelText('Operator of assertion 1'));
    expect(options()).toEqual(['<', '<=', '>', '>=', '==', '!=', 'in']);
    await user.keyboard('{Escape}');

    await pickOption(screen, 'Subject of assertion 1', { value: 'body' });
    await user.click(screen.getByLabelText('Operator of assertion 1'));
    expect(options()).toEqual(['==', '!=', 'contains', '!contains', 'matches', '!matches']);
    await user.keyboard('{Escape}');
    // The number typed for status is kept as the text to compare.
    expect(text()).toBe('ok=body == "200"');

    await pickOption(screen, 'Subject of assertion 1', { value: 'header' });
    await user.click(screen.getByLabelText('Operator of assertion 1'));
    expect(options()).toEqual(['==', '!=', 'contains', '!contains', 'matches', '!matches', 'exists', '!exists']);
    await user.keyboard('{Escape}');
    await user.type(screen.getByLabelText('Argument of assertion 1'), 'Server');
    await pickOption(screen, 'Operator of assertion 1', { value: 'exists' });
    expect(screen.queryByLabelText('Value of assertion 1')).not.toBeInTheDocument();
    expect(text()).toBe('ok=header("Server") exists');
  });

  it('edits the raw list behind the text toggle and reads it back into rows', async () => {
    const user = userEvent.setup();
    render(<Harness initial="ok=status == 200" />);
    await user.click(screen.getByRole('button', { name: 'Edit as text' }));
    const area = screen.getByLabelText('Assertions');
    expect(area).toHaveValue('ok=status == 200');
    await user.clear(area);
    await user.type(area, 'ok=status    in 200..299{Enter}b=body   contains "up" @crit{Enter}x=nonsense');
    await user.click(screen.getByRole('button', { name: 'Edit as rows' }));
    // A line the grammar refuses keeps the text.
    expect(screen.getByText('Line 3 is not an assertion. Correct it here before switching to rows.')).toBeInTheDocument();
    expect(screen.getByLabelText('Assertions')).toBeInTheDocument();

    await user.type(area, '{Backspace>16}');
    await user.click(screen.getByRole('button', { name: 'Edit as rows' }));
    expect(screen.getAllByTestId('assertion-row')).toHaveLength(2);
    expect(screen.getByLabelText('Value of assertion 2')).toHaveValue('up');
    // Back in rows, the text is the canonical one, and it round-trips.
    expect(text()).toBe('ok=status in 200..299\nb=body contains "up"');
    await user.click(screen.getByRole('button', { name: 'Edit as text' }));
    expect(screen.getByLabelText('Assertions')).toHaveValue('ok=status in 200..299\nb=body contains "up"');
  });

  it('opens on the text when a stored entry is not one it can show as a row', () => {
    render(<Harness initial={'ok=status == 200\nbroken'} />);
    expect(screen.getByLabelText('Assertions')).toHaveValue('ok=status == 200\nbroken');
    expect(screen.queryByTestId('assertion-row')).not.toBeInTheDocument();
  });

  it('shows an API error under the row whose entry it names', () => {
    render(
      <Harness
        initial={'ok=status == 200\nre=body matches "("'}
        serverError={'invalid input: http: assertions must be name=assertion such as ok=status == 200 (see the docs): "re=body matches \\"(\\"": the regular expression does not compile'}
      />,
    );
    const [first, second] = screen.getAllByTestId('assertion-row');
    expect(within(second).getByRole('alert')).toHaveTextContent('the regular expression does not compile');
    expect(within(first).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('marks an incomplete row once the field is refused', async () => {
    const user = userEvent.setup();
    render(<Harness initial="ok=status == 200" error="every entry must be name=assertion" />);
    await user.click(screen.getByRole('button', { name: 'Add assertion' }));
    await user.type(screen.getByLabelText('Name of assertion 2'), 'x');
    const second = screen.getAllByTestId('assertion-row')[1];
    expect(within(second).getByRole('alert')).toHaveTextContent('Not a complete assertion yet');
    expect(within(screen.getAllByTestId('assertion-row')[0]).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('speaks German', () => {
    render(
      <I18nProvider locale="de">
        <AssertionEditor id="a" label="Zusicherungen" value="ok=status == 200 @warn" onChange={() => {}} />
      </I18nProvider>,
    );
    expect(screen.getByLabelText('Gegenstand der Zusicherung 1')).toHaveValue('status (Statuscode)');
    expect(screen.getByLabelText('Schweregrad der Zusicherung 1')).toHaveValue('Warnung');
    expect(screen.getByRole('button', { name: 'Als Text bearbeiten' })).toBeInTheDocument();
  });
});
