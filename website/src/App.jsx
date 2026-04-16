import Hero from './components/Hero'
import ThreatLandscape from './components/ThreatLandscape'
import HowItWorks from './components/HowItWorks'
import Features from './components/Features'
import SandboxMode from './components/SandboxMode'
import QuickStart from './components/QuickStart'
import Footer from './components/Footer'
import './App.css'

export default function App() {
  return (
    <div className="app">
      <Hero />
      <ThreatLandscape />
      <HowItWorks />
      <Features />
      <SandboxMode />
      <QuickStart />
      <Footer />
    </div>
  )
}
