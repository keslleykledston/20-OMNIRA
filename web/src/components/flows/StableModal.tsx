import { useCallback, useRef, type ComponentProps } from 'react'
import { Modal } from '../primitives'

// A primitiva Modal refaz o foco inicial toda vez que a identidade de `onClose` muda, e um `onClose` inline muda a cada
// renderização: com um campo de texto dentro, o foco volta ao botão "Fechar" a cada tecla e o texto digitado se perde.
// Este invólucro entrega à primitiva uma função estável que chama sempre o `onClose` mais recente.
export default function StableModal(props: ComponentProps<typeof Modal>) {
  const latest = useRef(props.onClose)
  latest.current = props.onClose
  const stable = useCallback(() => latest.current(), [])
  return <Modal {...props} onClose={stable} />
}
