'use strict';

const {contextBridge, ipcRenderer} = require('electron');

contextBridge.exposeInMainWorld('desktop', {
  chooseFolder: current => ipcRenderer.invoke('app:folder', current),
  appearance: (theme, glass) => ipcRenderer.invoke('app:appearance', theme, glass),
  openSniffer: () => ipcRenderer.invoke('app:sniffer'),
  paste: () => ipcRenderer.invoke('app:paste'),
  quit: () => ipcRenderer.invoke('app:quit'),
  progress: (fraction, active) => ipcRenderer.invoke('app:progress', fraction, active),
  onNotice: callback => { ipcRenderer.on('app:notice', (_event, message) => callback(message)); },
  onCommand: callback => { ipcRenderer.on('app:command', (_event, name) => callback(name)); },
});
