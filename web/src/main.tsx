import ReactDOM from 'react-dom/client'
import App from './App'
import { installActingInterceptor } from './lib/acting'
import './index.css'

installActingInterceptor()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <App />
)
