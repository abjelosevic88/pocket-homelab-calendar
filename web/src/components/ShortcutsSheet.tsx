import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Navigate', keys: [[['t'], 'Today'], [['j'], 'Next period'], [['k'], 'Previous period'], [['→'], 'Next period'], [['←'], 'Previous period'], [['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands']] },
  { title: 'Views', keys: [[['d'], 'Day'], [['x'], '3 days'], [['w'], 'Week'], [['m'], 'Month'], [['y'], 'Year'], [['a'], 'Agenda'], [['p'], 'People']] },
  { title: 'Events', keys: [[['c'], 'Create an event'], [['q'], 'Quick add'], [['e'], 'Edit the selected event'], [['⌫'], 'Delete the selected event'], [['⌘', 'Z'], 'Undo the last move or delete'], [['esc'], 'Close / cancel a drag'], [['⌘', '↵'], 'Save in the editor']] },
  { title: 'Help', keys: [[['?'], 'This sheet']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
      <p className="muted small mt">On touch screens: long-press an event to drag it, swipe left or right to change the period.</p>
    </Modal>
  )
}
