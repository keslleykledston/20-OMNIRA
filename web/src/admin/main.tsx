import ReactDOM from 'react-dom/client'
import AdminApp from './AdminApp'
import { installActingInterceptor } from '../lib/acting'
import '../index.css'

installActingInterceptor()

ReactDOM.createRoot(document.getElementById('root')!).render(<AdminApp />)
