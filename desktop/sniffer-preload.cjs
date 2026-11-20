'use strict';

const {contextBridge, ipcRenderer} = require('electron');

contextBridge.exposeInMainWorld('sniffer', {
  state: () => ipcRenderer.invoke('sniff:state'),
  navigate: url => ipcRenderer.invoke('sniff:navigate', url),
  copy: url => ipcRenderer.invoke('sniff:copy', url),
  action: name => ipcRenderer.invoke('sniff:action', name),
  onState: callback => { ipcRenderer.on('sniff:changed', (_event, state) => callback(state)); },
});
