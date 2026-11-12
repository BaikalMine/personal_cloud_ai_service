import { DatabaseSync } from 'node:sqlite';

export class UploadRetention {
  constructor(path,{now=()=>Date.now(),hours=24}={}){
    this.now=now;this.lifetime=hours*3600000;this.db=new DatabaseSync(path);
    this.db.exec('PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS uploads(user INTEGER NOT NULL,inode TEXT NOT NULL,expires INTEGER NOT NULL,PRIMARY KEY(user,inode));');
  }
  register(user,inode){this.db.prepare('INSERT OR IGNORE INTO uploads(user,inode,expires) VALUES(?,?,?)').run(user,inode,this.now()+this.lifetime);}
  expired(user){return this.db.prepare('SELECT inode FROM uploads WHERE user=? AND expires<=?').all(user,this.now()).map(x=>x.inode);}
  next(user,inode){return this.db.prepare('SELECT expires FROM uploads WHERE user=? AND inode=?').get(user,inode)?.expires;}
  close(){this.db.close();}
}
